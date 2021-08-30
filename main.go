package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"faasd-agent/pkg/handlers"
	"faasd-agent/pkg/proxy"
	"io/ioutil"
	"net/http"
	"os"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru"

	"faasd-agent/pkg/config"
	"faasd-agent/pkg/types"
	pb "faasd-agent/proto/agent"
	"log"
	"net"

	"github.com/containerd/containerd"
	"google.golang.org/grpc"
)

const (
	port                 = ":50051"
	MaxCacheItem         = 8
	FileCacheSize        = 5
	UseCache             = true
	SupportCacheChecking = true
)

type Function struct {
	name        string
	namespace   string
	image       string
	pid         uint32
	replicas    int
	IP          string
	labels      map[string]string
	annotations map[string]string
}

// server is used to implement helloworld.GreeterServer.
type server struct {
	pb.UnimplementedTasksRequestServer
}

var Cache *lru.Cache
var mutex sync.Mutex
var cacheHit uint
var cacheMiss uint

// SayHello implements helloworld.GreeterServer
func (s *server) TaskAssign(ctx context.Context, in *pb.TaskRequest) (*pb.TaskResponse, error) {

	if len(in.RequestHashes) > 0 {
		res := &pb.TaskResponse{Message: "OK", Responses: make([][]byte, len(in.RequestHashes))}
		if SupportCacheChecking {
			log.Printf("New Task checking cache, len(requests): %v", len(in.RequestHashes))
			for i, reqHash := range in.RequestHashes {
				value, found := Cache.Get(reqHash)
				if found {
					res.Responses[i] = value.([]byte)
				}
			}
			return res, nil
		}

	}
	log.Printf("New Task Received: %v", in.FunctionName)
	var sReqHash string

	req, err := unserializeReq(in.SerializeReq)
	if err != nil {
		log.Printf("failed unserializeReq:  %s: %s\n", in.FunctionName, err.Error())
		return nil, err
	}

	// ******** cache
	if UseCache {
		bodyBytes, err := ioutil.ReadAll(req.Body)
		if err != nil {
			log.Println("read request bodey error :", err.Error())
		}
		req.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes))
		sReqHash = hash(append([]byte(in.FunctionName), bodyBytes...))
		mutex.Lock()
		res, found := Cache.Get(sReqHash)
		if found {
			cacheHit++
			mutex.Unlock()
			log.Printf("Found in cache: %v, cacheHit: %v", in.FunctionName, cacheHit)
			return &pb.TaskResponse{Message: "OK", Response: res.([]byte)}, nil
		}

		mutex.Unlock()
	}

	faasConfig, providerConfig, err := config.ReadFromEnv(types.OsEnv{})
	if err != nil {
		log.Printf("failed to ReadFromEnv: %v", err)
		return nil, err
	}
	// log.Printf("Containerd socket: %v", providerConfig.Sock)
	client, err := containerd.New(providerConfig.Sock)
	if err != nil {
		log.Printf("failed containerd.New:  %s: %s\n", in.FunctionName, err.Error())
		return nil, err
	}
	defer client.Close()

	invokeResolver := handlers.NewInvokeResolver(client)

	functionAddr, _, resolveErr := invokeResolver.Resolve(in.FunctionName)
	if resolveErr != nil {
		// TODO: Should record the 404/not found error in Prometheus.
		log.Printf("resolver error: cannot find %s: %s\n", in.FunctionName, resolveErr.Error())
		return nil, resolveErr
	}

	tryCounter := 0
	var sRes []byte
	var seconds time.Duration
	for {
		proxyClient := proxy.NewProxyClientFromConfig(*faasConfig)

		proxyReq, err := proxy.BuildProxyRequest(req, functionAddr, in.ExteraPath)
		if err != nil {
			// function.CloseChannel <- struct{}{}
			log.Printf("failed proxyReq:  %s: %s\n", in.FunctionName, err.Error())
			return nil, err
		}
		if proxyReq.Body != nil {
			defer proxyReq.Body.Close()
		}

		start := time.Now()
		response, err := proxyClient.Do(proxyReq.WithContext(ctx))
		seconds = time.Since(start)
		// function.CloseChannel <- struct{}{}
		if err != nil {
			log.Printf("error with proxy %s request to: %s, %s\n", in.FunctionName, proxyReq.URL.String(), err.Error())
			tryCounter++
			if tryCounter > 2 {
				log.Printf("****** Second TIme error with proxy %s request to: %s, %s\n", in.FunctionName, proxyReq.URL.String(), err.Error())
				return nil, err
			}
			// defer response.Body.Close()
			continue
		}
		defer response.Body.Close()
		//bodyBytes, err := ioutil.ReadAll(response.Body)
		//if err != nil {
		//	log.Printf("error in reading response body: %s \n", err)
		//	return nil, err
		//}
		//
		//bodyString := string(bodyBytes)

		sRes, err = captureRequestData(response)
		if err != nil {
			log.Printf("error in serializing response: %s \n", err)
			return nil, err
		}

		break
	}

	// *************** cache
	if UseCache {
		mutex.Lock()
		Cache.Add(sReqHash, sRes)
		mutex.Unlock()
	}
	cacheMiss++
	//log.Printf("Mohammad function name: %s, result: %s \n",in.FunctionName, bodyString)
	log.Printf("Mohammad %s took %f seconds, sRes length: %v, cacheMiss : %v, sReqHash : %v \n", in.FunctionName,
		seconds.Seconds(), len(sRes), cacheMiss, sReqHash)
	// for metric := range function.MetricChannel {
	// 	err := printMetricData(metric)
	// 	if err != nil {
	// 		continue
	// 	}
	// }
	return &pb.TaskResponse{Message: "OK", Response: sRes}, nil
}

func main() {
	cacheHit = 0
	cacheMiss = 0

	if len(os.Args) < 2 {
		log.Fatalf("Provid port nummber")
	}
	Cache, _ = lru.New(MaxCacheItem)
	lis, err := net.Listen("tcp", ":"+os.Args[1])
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	RunProxy(os.Args[2])

	s := grpc.NewServer()
	pb.RegisterTasksRequestServer(s, &server{})
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}

func unserializeReq(sReq []byte) (*http.Request, error) {
	b := bytes.NewBuffer(sReq)
	r := bufio.NewReader(b)
	req, err := http.ReadRequest(r)
	if err != nil {
		return nil, err
	}
	return req, nil
}

func captureRequestData(res *http.Response) ([]byte, error) {
	var b = &bytes.Buffer{} // holds serialized representation
	//var tmp *http.Request
	var err error
	if err = res.Write(b); err != nil { // serialize request to HTTP/1.1 wire format
		return nil, err
	}
	//var reqSerialize []byte

	return b.Bytes(), nil
	//r := bufio.NewReader(b)
	//if tmp, err = http.ReadRequest(r); err != nil { // deserialize request
	//	return nil,err
	//}
	//*req = *tmp // replace original request structure
	//return nil
}

func hash(data []byte) string {
	h := sha1.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}
