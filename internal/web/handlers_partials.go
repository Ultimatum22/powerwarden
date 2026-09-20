package web

import "net/http"

func (s *Server) handlePartialHost(w http.ResponseWriter, r *http.Request) {
	s.renderFragment(w, "partial_host", s.hostView(r.Context()))
}

func (s *Server) handlePartialWeather(w http.ResponseWriter, r *http.Request) {
	s.renderFragment(w, "partial_weather", s.weatherView(r.Context()))
}

func (s *Server) handlePartialGuests(w http.ResponseWriter, r *http.Request) {
	guests, err := s.guestViews(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderFragment(w, "partial_guests", guests)
}
