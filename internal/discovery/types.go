package discovery

import "time"

type Config struct {
	Enabled               bool
	OllamaURL             string
	OllamaModel           string
	RequestTimeout        time.Duration
	HealthTimeout         time.Duration
	OpenAIFallbackEnabled bool
	OpenAIAPIKey          string
	OpenAIBaseURL         string
	OpenAIModel           string
	OpenAITimeout         time.Duration
}

type Candidate struct {
	ID        int64    `json:"id"`
	VideoID   string   `json:"video_id"`
	Title     string   `json:"title"`
	Story     string   `json:"story"`
	Actresses []string `json:"performers"`
	Genres    []string `json:"tags"`
	Studio    string   `json:"studio"`
	Local     bool     `json:"local"`
	Played    int      `json:"play_count"`
	Orgasms   int      `json:"orgasm_count"`
	Evidence  []string `json:"grounding_evidence"`
	Subtitle  string   `json:"subtitle_excerpt,omitempty"`
}

type Rank struct {
	ID     int64    `json:"id"`
	Score  float64  `json:"score"`
	Reason string   `json:"reason"`
	Pools  []string `json:"pools"`
}

type Hints struct {
	ReleaseID   string   `json:"release_id"`
	Title       string   `json:"title"`
	Studio      string   `json:"studio"`
	Performers  []string `json:"performers"`
	SearchTerms []string `json:"search_terms"`
	Confidence  float64  `json:"confidence"`
}

type OllamaStatus struct {
	Reachable      bool     `json:"reachable"`
	ModelAvailable bool     `json:"model_available"`
	URL            string   `json:"url"`
	Model          string   `json:"model"`
	Models         []string `json:"models,omitempty"`
	Message        string   `json:"message"`
}

type Result struct {
	Ranks    []Rank
	Provider string
	Skipped  bool
	Status   string
}
