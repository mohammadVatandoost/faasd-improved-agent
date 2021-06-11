package handlers

import (
	"fmt"
	// "log"
	"net/url"

	"github.com/containerd/containerd"
)

const watchdogPort = 8080

type InvokeResolver struct {
	client *containerd.Client
}

func NewInvokeResolver(client *containerd.Client) *InvokeResolver {
	return &InvokeResolver{client: client}
}

func (i *InvokeResolver) Resolve(functionName string) (url.URL, Function, error) {
	// log.Printf("Function handler Resolve: %q\n", functionName)

	function, err := GetFunction(i.client, functionName)
	if err != nil {
		return url.URL{}, Function{}, fmt.Errorf("%s not found", functionName)
	}

	serviceIP := function.IP

	urlStr := fmt.Sprintf("http://%s:%d", serviceIP, watchdogPort)

	urlRes, err := url.Parse(urlStr)
	if err != nil {
		return url.URL{}, Function{}, err
	}

	return *urlRes, function, nil
}
