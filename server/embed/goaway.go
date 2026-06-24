// Copyright 2024 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package embed

import (
	"math/rand"
	"net/http"
	"sync"
)

// goawayDecider decides if server should send a GOAWAY
type goawayDecider interface {
	goaway(r *http.Request) bool
}

var (
	randPool = &sync.Pool{
		New: func() interface{} {
			return rand.New(rand.NewSource(rand.Int63()))
		},
	}
)

// withProbabilisticGoaway returns an http.Handler that sends GOAWAY probabilistically
// according to the given chance for HTTP/2 requests. After the client receives GOAWAY,
// in-flight long-running requests will not be influenced, and new requests
// will use a new TCP connection to re-balance to another server behind the load balancer.
func withProbabilisticGoaway(inner http.Handler, chance float64) http.Handler {
	return &goawayHandler{
		handler: inner,
		decider: &probabilisticGoawayDecider{
			chance: chance,
			next: func() float64 {
				rnd := randPool.Get().(*rand.Rand)
				ret := rnd.Float64()
				randPool.Put(rnd)
				return ret
			},
		},
	}
}

type goawayHandler struct {
	handler http.Handler
	decider goawayDecider
}

func (h *goawayHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Proto == "HTTP/2.0" && h.decider.goaway(r) {
		w.Header().Set("Connection", "close")
	}
	h.handler.ServeHTTP(w, r)
}

type probabilisticGoawayDecider struct {
	chance float64
	next   func() float64
}

func (p *probabilisticGoawayDecider) goaway(_ *http.Request) bool {
	return p.next() < p.chance
}
