# Envoy control plane Example


I wanted to understand in detail how the Envoy control plane worked. I found great articles to get me going, specially this one: [Envoy hello World](https://medium.com/@salmaan.rashid/envoy-control-plane-hello-world-2f49b2865f29I)

As far code examples, there are two gems that have reat building blocks:

* [Istio's Envoy Proxy Server testing code](https://github.com/istio/istio/blob/master/vendor/github.com/envoyproxy/go-control-plane/pkg/test/server.go)
* [Envoy's Server Testing package](https://github.com/envoyproxy/go-control-plane/blob/master/pkg/test/server.go),   

So, why go through the exercise? Several reasons:

* The code the article above did not work anymore due to changes in the Envoy API code
* I wanted to learn by doing it mostly from scratch
* I wanted to use Golang's core library as much as possible. In other words, the least number of third-party dependencies.




