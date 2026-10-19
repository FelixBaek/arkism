package memory

import "testing"

func TestValidCrewCoverUpload(t *testing.T) {
	ok := &CrewCover{ID: "mine", URL: "data:image/jpeg;base64,AAAA", Author: "내 사진"}
	if !validCrewCover(ok) || !validCrewCover(nil) {
		t.Fatal("own photo or no cover must be valid")
	}
	for name, c := range map[string]*CrewCover{
		"png":       {ID: "mine", URL: "data:image/png;base64,AAAA", Author: "내 사진"},
		"too big":   {ID: "mine", URL: "data:image/jpeg;base64," + string(make([]byte, crewCoverUploadMax)), Author: "내 사진"},
		"thumb":     {ID: "mine", URL: "data:image/jpeg;base64,AAAA", Thumb: "x", Author: "내 사진"},
		"no author": {ID: "mine", URL: "data:image/jpeg;base64,AAAA"},
		"other id":  {ID: "abc", URL: "data:image/jpeg;base64,AAAA", Thumb: "data:image/jpeg;base64,AAAA", Author: "a", AuthorURL: "https://unsplash.com/x"},
	} {
		if validCrewCover(c) {
			t.Errorf("%s: should be invalid", name)
		}
	}
}
