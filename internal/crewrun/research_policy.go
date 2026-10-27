package crewrun

type CrewResearch struct {
	QuickNode        string `json:"quick_node"`
	DeepNode         string `json:"deep_node"`
	DeepAlias        string `json:"deep_alias"`
	EvidenceStrategy string `json:"evidence_strategy"`
	HelperOrigin     string `json:"helper_origin"`
}
