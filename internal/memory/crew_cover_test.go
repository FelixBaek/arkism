package memory

import (
	"encoding/base64"
	"testing"
)

func TestValidCrewCoverUpload(t *testing.T) {
	jpeg := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10})
	ok := &CrewCover{ID: "mine", URL: jpeg, Author: "내 사진"}
	if !validCrewCover(ok) || !validCrewCover(nil) {
		t.Fatal("own photo or no cover must be valid")
	}
	for name, c := range map[string]*CrewCover{
		"png":       {ID: "mine", URL: "data:image/png;base64,AAAA", Author: "내 사진"},
		"not jpeg":  {ID: "mine", URL: "data:image/jpeg;base64,AAAA", Author: "내 사진"},
		"bad b64":   {ID: "mine", URL: jpeg + `")`, Author: "내 사진"},
		"blank by":  {ID: "mine", URL: jpeg, Author: " "},
		"too big":   {ID: "mine", URL: "data:image/jpeg;base64," + string(make([]byte, crewCoverUploadMax)), Author: "내 사진"},
		"thumb":     {ID: "mine", URL: jpeg, Thumb: "x", Author: "내 사진"},
		"no author": {ID: "mine", URL: jpeg},
		"other id":  {ID: "abc", URL: "data:image/jpeg;base64,AAAA", Thumb: "data:image/jpeg;base64,AAAA", Author: "a", AuthorURL: "https://unsplash.com/x"},
	} {
		if validCrewCover(c) {
			t.Errorf("%s: should be invalid", name)
		}
	}
}
