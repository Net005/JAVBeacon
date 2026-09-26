package discovery

import "time"

type Config struct {
	Enabled                bool
	PrimaryProvider        string
	OllamaURL              string
	OllamaModel            string
	RequestTimeout         time.Duration
	HealthTimeout          time.Duration
	OpenAIFallbackEnabled  bool
	OpenAIAPIKey           string
	OpenAIBaseURL          string
	OpenAIModel            string
	OpenAITimeout          time.Duration
	OpenAIIncludeSubtitles bool
	// SubtitleWeightNoStory and SubtitleWeightWithStory control how heavily
	// the ranking prompt tells the model to lean on subtitle_excerpt versus
	// the story field, as an explicit 0-100 emphasis: 0 means "ignore
	// subtitles for this case", 100 means "treat subtitles as the primary/
	// equal narrative source". They are configured separately because the
	// two situations are different: SubtitleWeightNoStory applies to
	// candidates with no story field at all (mostly JAVLibrary-sourced),
	// where subtitles are often the only narrative evidence available;
	// SubtitleWeightWithStory applies to candidates that already have a
	// story field (mostly Akiba/GIGA), where subtitles - already
	// AI-translated and often carrying real narrative detail - can be
	// weighted anywhere from a minor supplement up to a fully fair, co-equal
	// source alongside the story. The production caller (discoveryAIConfig)
	// always resolves these from settings with the documented 100/50
	// fallback already applied, so these fields carry no separate "unset"
	// state - a bare zero-value Config (as some tests use) resolves to 0/0,
	// which only affects prompt phrasing, never validation.
	SubtitleWeightNoStory   int
	SubtitleWeightWithStory int
}

type TasteSignals struct {
	PreferredPerformers []string `json:"preferred_performers"`
	PreferredThemes     []string `json:"preferred_themes"`
	PreferredStudio     string   `json:"preferred_studio"`
	PreferredLabel      string   `json:"preferred_label"`
	WatchedTextThemes   []string `json:"watched_title_story_themes"`
	FreshDiscovery      bool     `json:"fresh_discovery"`
}

type Candidate struct {
	ID                int64    `json:"id"`
	VideoID           string   `json:"video_id"`
	Title             string   `json:"title"`
	Story             string   `json:"story"`
	Actresses         []string `json:"performers"`
	Genres            []string `json:"tags"`
	Studio            string   `json:"studio"`
	Label             string   `json:"label"`
	Director          string   `json:"director"`
	ReleaseDate       string   `json:"release_date"`
	Duration          string   `json:"duration"`
	Local             bool     `json:"local"`
	Played            int      `json:"play_count"`
	Orgasms           int      `json:"orgasm_count"`
	BaselineScore     float64  `json:"deterministic_score"`
	DiscoveryState    string   `json:"discovery_state"`
	SubtitleAvailable bool     `json:"subtitle_available"`
	// Evidence remains available for compatibility and server-side validation,
	// but is deliberately omitted from model JSON. Its old label-heavy strings
	// encouraged small models to copy internal UI text verbatim.
	Evidence []string     `json:"-"`
	Taste    TasteSignals `json:"taste_match"`
	// EligiblePools is computed deterministically by JAVBeacon from the
	// configured pool keywords and candidate metadata. Models may select only
	// from this list; an empty non-nil list means no pool is supported.
	EligiblePools []string `json:"eligible_pools"`
	Subtitle      string   `json:"subtitle_excerpt,omitempty"`
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
	Ranks             []Rank
	Provider          string
	AttemptedProvider string
	Usage             Usage
	Skipped           bool
	Status            string
}

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}
