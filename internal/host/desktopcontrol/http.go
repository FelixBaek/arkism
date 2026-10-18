package desktopcontrol

import "net/http"

func (g *Gate) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		release, err := g.Enter(Handler)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"desktop_admission_paused"}`))
			return
		}
		defer release()
		next.ServeHTTP(w, r)
	})
}
