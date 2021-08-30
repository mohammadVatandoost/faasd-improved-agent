package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"path/filepath"

	"github.com/fanap-infra/log"
	"github.com/gin-gonic/gin"
	lru "github.com/hashicorp/golang-lru"
)

type Server struct {
	Engine *gin.Engine
	Port   string
	Cache  *lru.Cache
}

type FileName struct {
	fileName string `uri:"fileName" binding:"required"`
}

func (s *Server) Run() {
	addr := "127.0.0.1:" + s.Port
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
	if err := c.ShouldBindUri(&fileName); err != nil {
		fmt.Println("Error it is not valid request, err:", err.Error())
		return
	}

	fmt.Println("fileName Request:", fileName.fileName)
	fileName.fileName = "I1.jpg"
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
		fmt.Println("find in cache, fileName:", fileName.fileName)
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
	Cache, _ = lru.New(FileCacheSize)
	server := &Server{Engine: gin.New(), Port: port, Cache: Cache}
	server.Run()
}
