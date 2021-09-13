package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"strconv"
	"sync/atomic"

	"github.com/fanap-infra/log"
	"github.com/gin-gonic/gin"
	lru "github.com/hashicorp/golang-lru"
)

type Server struct {
	Engine   *gin.Engine
	Port     string
	Cache    *lru.Cache
	CacheHit uint64
}

type FileName struct {
	fileName string `uri:"fileName" binding:"required"`
}

var serverProxy *Server

func (s *Server) Run() {
	addr := "0.0.0.0:" + s.Port
	fmt.Printf("Proxy runs on address: %s \n ", addr)

	s.Engine.GET("/assets/images/:fileName", s.NetworkRequests)

	srv := &http.Server{
		Addr:    addr,
		Handler: s.Engine,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Errorv("Error on listen HTTP Server", "error", err)
		}
	}()
}

func (s *Server) NetworkRequests(c *gin.Context) {
	var fileName FileName
	// if err := c.ShouldBindUri(&fileName); err != nil {
	// 	fmt.Println("Error it is not valid request, err:", err.Error())
	// 	return
	// }
	fileName.fileName = c.Param("fileName")

	fmt.Println("******* fileName Request:", fileName.fileName)
	// fileName.fileName = "I1.jpg"
	// director := func(req *http.Request) {
	// 	r := c.Request
	// 	req.URL.Scheme = "http"
	// 	req.URL.Host = "mvatandoosts.ir"
	// 	req.URL.Path = fmt.Sprintf("/assets/images/%s", fileName.fileName)
	// 	req.RequestURI = req.URL.Path
	// 	req.Header["my-header"] = []string{r.Header.Get("my-header")}
	// 	delete(req.Header, "My-Header")
	// 	fmt.Println("req.URL.Path:", req.URL.Path)
	// 	fmt.Println("req.URL.String():", req.URL.String())
	// }

	res, found := s.Cache.Get(fileName.fileName)

	if found {
		atomic.AddUint64(&s.CacheHit, 1)
		fmt.Printf("find in cache, fileName: %v, CacheHit: %v \n", fileName.fileName, s.CacheHit)

		c.Header("Content-Description", "File Transfer")
		c.Header("Content-Transfer-Encoding", "binary")
		c.Header("Content-Disposition", "attachment; filename="+filepath.Base(fileName.fileName))
		c.Header("Content-Type", "application/octet-stream")
		c.Data(http.StatusOK, "application/octet-stream", res.([]byte))
		return
	}

	fmt.Println("does not find in cache, fileName:", fileName.fileName)
	resp, err := http.Get("http://mvatandoosts.ir/assets/images/" + fileName.fileName)
	if err != nil {
		fmt.Println("can not get request value, err:", err.Error())
		return
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("can not get read value, err:", err.Error())
	}

	s.Cache.Add(fileName.fileName, body)

	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Transfer-Encoding", "binary")
	c.Header("Content-Disposition", "attachment; filename="+filepath.Base(fileName.fileName))
	c.Header("Content-Type", "application/octet-stream")
	c.Data(http.StatusOK, "application/octet-stream", body)
}

func RunProxy(port string) {
	// GetSizeOfFiles()
	Cache, _ = lru.New(FileCacheSize)
	serverProxy = &Server{Engine: gin.New(), Port: port, Cache: Cache, CacheHit: 0}
	serverProxy.Run()
}

func GetSizeOfFiles() {
	counter := 11
	for {
		fileAddress := "http://mvatandoosts.ir/assets/images/I" + strconv.Itoa(counter) + ".jpg"
		resp, err := http.Get(fileAddress)
		if err != nil {
			fmt.Println("can not get request value, err:", err.Error())
			return
		}

		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			fmt.Println("can not get read value, err:", err.Error())
			return
		}
		fmt.Printf("fileName:= %v, fileSize: %v \n", fileAddress, len(body))
		counter++
		if counter > 30 {
			break
		}
	}
}
