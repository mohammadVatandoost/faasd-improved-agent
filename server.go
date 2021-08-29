package main

import (
	"fmt"
	"net/http"

	"github.com/fanap-infra/log"
	"github.com/gin-gonic/gin"
)

type Server struct {
	Engine *gin.Engine
	Port   string
}

func (s *Server) Run() {
	addr := "127.0.0.1:" + s.Port
	fmt.Printf("Proxy runs on address: %s \n ", addr)

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

func RunProxy(port string) {
	server := &Server{Engine: gin.New(), Port: port}
	server.Run()
}
