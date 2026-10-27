package memory

type CrewResearch struct {
	QuickNode        string `json:"quick_node" bson:"quick_node"`
	DeepNode         string `json:"deep_node" bson:"deep_node"`
	DeepAlias        string `json:"deep_alias" bson:"deep_alias"`
	EvidenceStrategy string `json:"evidence_strategy" bson:"evidence_strategy"`
	HelperOrigin     string `json:"helper_origin" bson:"helper_origin"`
}
