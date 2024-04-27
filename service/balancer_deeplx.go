package service

import (
	"context"
	"deeplx-local/domain"
	"github.com/sourcegraph/conc/pool"
	"log"
	"sync"
	"time"
)

type Server struct {
	URL           string
	Weight        int
	CurrentWeight int
	ResponseTime  time.Duration
}

type LoadBalancer struct {
	Servers       []*Server
	mutex         sync.Mutex
	deepLXService *DeepLXService
}

// NewLoadBalancer 负载均衡 装饰器模式包了一层service
func NewLoadBalancer(service *DeepLXService) TranslateService {
	servers := make([]*Server, len(*service.validList))
	for i, url := range *service.validList {
		servers[i] = &Server{URL: url, Weight: 1, CurrentWeight: 1}
	}
	return &LoadBalancer{Servers: servers, deepLXService: service}
}

func (lb *LoadBalancer) GetTranslateData(trReq domain.TranslateRequest) domain.TranslateResponse {
	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()
	resultChan := make(chan domain.TranslateResponse, 5)

	contextPool := pool.New().WithContext(ctx).WithMaxGoroutines(5)
	for i := 0; i < 5; i++ {
		contextPool.Go(func(ctx context.Context) error {
			server := lb.getServer()
			var trResult domain.TranslateResponse
			start := time.Now()
			response, err := lb.deepLXService.client.R().
				SetContext(ctx).
				SetBody(trReq).
				SetSuccessResult(&trResult).
				Post(server.URL)
			elapsed := time.Since(start)
			lb.updateResponseTime(server, elapsed)

			if err != nil {
				return err
			}
			response.Body.Close()

			if trResult.Code == 200 && len(trResult.Data) > 0 {
				resultChan <- trResult
				cancelFunc()
			}
			return nil
		})
	}

	go func() {
		_ = contextPool.Wait()
		if _, ok := <-resultChan; !ok { // 如果通道已经关闭，直接返回
			return
		}
		close(resultChan)
	}()

	select {
	case r := <-resultChan:
		defer cancelFunc()
		return r
	case <-ctx.Done():
		log.Println("all requests failed")
	}
	return domain.TranslateResponse{}
}

func (lb *LoadBalancer) getServer() *Server {
	lb.mutex.Lock()
	defer lb.mutex.Unlock()

	var bestServer *Server
	total := 0

	for _, server := range lb.Servers {
		server.CurrentWeight += server.Weight
		total += server.Weight

		if bestServer == nil || server.CurrentWeight > bestServer.CurrentWeight {
			bestServer = server
		}
	}

	if bestServer != nil {
		bestServer.CurrentWeight -= total
	}

	return bestServer
}

func (lb *LoadBalancer) updateResponseTime(server *Server, responseTime time.Duration) {
	lb.mutex.Lock()
	defer lb.mutex.Unlock()

	server.ResponseTime = responseTime
	server.Weight = int(time.Second / (responseTime + 1))
}
