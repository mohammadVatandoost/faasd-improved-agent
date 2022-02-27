package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/csv"
	"encoding/hex"
	"faasd-agent/pkg/handlers"
	"faasd-agent/pkg/proxy"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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
	MaxCacheItem         = 4
	FileCacheSize        = 4
	UseCache             = true
	SupportCacheChecking = false
	FileCaching          = false
	WriteToCSV           = false
	IP                   = "192.168.43.220"
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
	csvWriter *csv.Writer
	filesSize map[string]string
}

var Cache *lru.Cache
var mutex sync.Mutex
var cacheHit uint64
var cacheMiss uint
var cacheHitFault int64
var cacheHitRequests int64
var totalReceiveNetworkTime int64
var totalExecutionTime int64
var numberOfTasks int64
var numberOfReadFile int64

// SayHello implements helloworld.GreeterServer
func (s *server) TaskAssign(ctx context.Context, in *pb.TaskRequest) (*pb.TaskResponse, error) {
	receivingNetworkDelay := time.Now().UnixNano() - in.TimeNanoSecond
	atomic.AddInt64(&numberOfTasks, 1)

	if in.CacheHit {
		atomic.AddInt64(&cacheHitRequests, 1)
	}

	if len(in.RequestHashes) > 0 {
		res := &pb.TaskResponse{Message: "OK", Responses: make([][]byte, len(in.RequestHashes))}
		if SupportCacheChecking {
			log.Printf("New Task checking cache, len(requests): %v \n", len(in.RequestHashes))
			// cacheHit := 0
			for i, reqHash := range in.RequestHashes {
				if FileCaching {
					reqHash = strings.Replace(reqHash, "http://mvatandoosts.ir/assets/images/", "", 1)
					_, found := serverProxy.Cache.Get(reqHash)
					if found {
						res.Responses[i] = make([]byte, 5)
						atomic.AddUint64(&cacheHit, 1)
					}
					// log.Printf("checking cache, cacheHit: %v, reqHash: %v \n", cacheHit, reqHash)
				} else {
					value, found := Cache.Get(reqHash)
					if found {
						res.Responses[i] = value.([]byte)
						atomic.AddUint64(&cacheHit, 1)
					}

				}

			}
			log.Printf("checking cache, cacheHit: %v \n", cacheHit)
			return res, nil
		}
	}
	atomic.AddInt64(&totalReceiveNetworkTime, receivingNetworkDelay)
	log.Printf("New Task Received: %v, totalReceiveNetworkTime: %v, numberOfTasks: %v, receivingNetworkDelay: %v",
		in.FunctionName, totalReceiveNetworkTime, numberOfTasks, receivingNetworkDelay)
	var sReqHash string
	var fInputs string
	req, err := unserializeReq(in.SerializeReq)
	if err != nil {
		log.Printf("failed unserializeReq:  %s: %s\n", in.FunctionName, err.Error())
		return nil, err
	}

	bodyBytes, err := ioutil.ReadAll(req.Body)
	if err != nil {
		log.Println("read request bodey error :", err.Error())
	}
	req.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes))

	// ******** cache
	if UseCache && !FileCaching {

		sReqHash = hash(append([]byte(in.FunctionName), bodyBytes...))
		mutex.Lock()
		res, found := Cache.Get(sReqHash)

		if found {
			cacheHit++
			mutex.Unlock()
			log.Printf("Found in cache: %v, cacheHit: %v, cacheHitFault: %v, cacheHitRequests: %v",
				in.FunctionName, cacheHit, cacheHitFault, cacheHitRequests)
			return &pb.TaskResponse{Message: "OK", Response: res.([]byte)}, nil
		}

		if in.CacheHit {
			cacheHitFault++
		}

		mutex.Unlock()
	}

	if FileCaching {
		bodyBytes, err := ioutil.ReadAll(req.Body)
		if err != nil {
			log.Println("read request bodey error :", err.Error())
		}
		if len(bodyBytes) > 0 {
			fInputs = string(bodyBytes)
			fInputs = strings.ReplaceAll(fInputs, "mvatandoosts.ir", IP+":"+serverProxy.Port)
			// log.Printf("prepare inputs for proxy: %v, fInputs: %v", in.FunctionName, fInputs)
			bodyBytes = []byte(fInputs)
		}
		req.Body = ioutil.NopCloser(bytes.NewBuffer(bodyBytes))
	}

	faasConfig, providerConfig, err := config.ReadFromEnv(types.OsEnv{})
	if err != nil {
		log.Printf("failed to ReadFromEnv: %v", err)
		return nil, err
	}
	// log.Printf("Containerd socket: %v", providerConfig.Sock)

	tryCounter := 0
	var sRes []byte
	var seconds time.Duration
	for {
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

		start := time.Now()
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

		response, err := proxyClient.Do(proxyReq.WithContext(ctx))
		seconds = time.Since(start)
		// function.CloseChannel <- struct{}{}
		if err != nil {
			log.Printf("error with proxy %s request to: %s, %s, seconds: %v\n", in.FunctionName,
				proxyReq.URL.String(), err.Error(), seconds.Seconds())
			tryCounter++
			if tryCounter > 2 {
				log.Printf("****** Second TIme error with proxy %s request to: %s, err:%s, bodyBytes: %s, seconds: %v \n",
					in.FunctionName, proxyReq.URL.String(), err.Error(), fInputs, seconds.Seconds())
				return nil, err
			}
			// defer response.Body.Close()
			// time.Sleep(50 * time.Millisecond)
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
	atomic.AddInt64(&totalExecutionTime, seconds.Milliseconds())
	if in.FunctionName == "face-detect-pigo" || in.FunctionName == "face-blur" {
		atomic.AddInt64(&numberOfReadFile, 1)
	}
	// *************** cache
	if UseCache && !FileCaching {
		mutex.Lock()
		Cache.Add(sReqHash, sRes)
		mutex.Unlock()
		cacheMiss++
		log.Printf("Mohammad %s took %f seconds, sRes length: %v, cacheMiss : %v, sReqHash : %v, totalExecutionTime: %v, numberOfReadFile: %v, cacheHitFault: %v, , cacheHitRequests: %v \n",
			in.FunctionName, seconds.Seconds(), len(sRes), cacheMiss, sReqHash, totalExecutionTime, numberOfReadFile, cacheHitFault, cacheHitRequests)
	}

	if WriteToCSV {
		s.csvWriter.Write([]string{in.FunctionName, string(bodyBytes), s.filesSize[string(bodyBytes)],
			fmt.Sprint(seconds.Seconds())})
		s.csvWriter.Flush()
	}
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
	log.Printf("Cache Size: %v, UseCache: %v, SupportCacheChecking: %v, FileCaching: %v, FileCacheSize: %v \n",
		MaxCacheItem, UseCache, SupportCacheChecking, FileCaching, FileCacheSize)

	if len(os.Args) < 2 {
		log.Fatalf("Provid port nummber")
	}
	Cache, _ = lru.New(MaxCacheItem)
	lis, err := net.Listen("tcp", ":"+os.Args[1])
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	if FileCaching {
		RunProxy(os.Args[2])
	}

	s := grpc.NewServer()
	if WriteToCSV {
		csvfile, err := os.Create("benchmark_" + os.Args[1] + ".csv")

		if err != nil {
			log.Fatalf("failed creating file: %s", err)
		}
		csvwriter := csv.NewWriter(csvfile)
		csvwriter.Write([]string{"Function Name", "Input Name", "Input Size", "Execution Time"})
		csvwriter.Flush()
		defer func() {
			fmt.Println("Close CSV file")
			csvwriter.Flush()
			csvfile.Close()
		}()
		filesSize := make(map[string]string)
		filesSize["http://mvatandoosts.ir/assets/images/I11.jpg"] = "14153"
		filesSize["http://mvatandoosts.ir/assets/images/I12.jpg"] = "12305"
		filesSize["http://mvatandoosts.ir/assets/images/I13.jpg"] = "14162"
		filesSize["http://mvatandoosts.ir/assets/images/I14.jpg"] = "20925"
		filesSize["http://mvatandoosts.ir/assets/images/I15.jpg"] = "14751"
		filesSize["http://mvatandoosts.ir/assets/images/I16.jpg"] = "16041"
		filesSize["http://mvatandoosts.ir/assets/images/I17.jpg"] = "24567"
		filesSize["http://mvatandoosts.ir/assets/images/I18.jpg"] = "14542"
		filesSize["http://mvatandoosts.ir/assets/images/I19.jpg"] = "15014"
		filesSize["http://mvatandoosts.ir/assets/images/I20.jpg"] = "7889"
		filesSize["http://mvatandoosts.ir/assets/images/I21.jpg"] = "13851"
		filesSize["http://mvatandoosts.ir/assets/images/I22.jpg"] = "13977"
		filesSize["http://mvatandoosts.ir/assets/images/I23.jpg"] = "11010"
		filesSize["http://mvatandoosts.ir/assets/images/I24.jpg"] = "14768"
		filesSize["http://mvatandoosts.ir/assets/images/I25.jpg"] = "10680"
		filesSize["http://mvatandoosts.ir/assets/images/I26.jpg"] = "13269"
		filesSize["http://mvatandoosts.ir/assets/images/I27.jpg"] = "13337"
		filesSize["http://mvatandoosts.ir/assets/images/I28.jpg"] = "13017"
		filesSize["http://mvatandoosts.ir/assets/images/I29.jpg"] = "14955"
		filesSize["http://mvatandoosts.ir/assets/images/I30.jpg"] = "14013"
		pb.RegisterTasksRequestServer(s, &server{csvWriter: csvwriter, filesSize: filesSize})
	} else {
		pb.RegisterTasksRequestServer(s, &server{})
	}

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
