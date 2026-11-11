package memory

import "testing"

// A chain of rewordings must not join two lessons that do not match each other.
func TestLessonGroupingIsCompleteLinkage(t *testing.T) {
	a := "retry the upload after refreshing the token"
	b := "refresh the token, then retry the upload in smaller chunks"
	c := "send smaller chunks with a progress bar while waiting"
	if !sameLesson(a, b) || !sameLesson(b, c) || sameLesson(a, c) {
		t.Fatalf("fixture: a~b %v b~c %v a~c %v", sameLesson(a, b), sameLesson(b, c), sameLesson(a, c))
	}
	if sameLessons([]string{a, b}, c) || !sameLessons([]string{a}, b) {
		t.Fatal("a lesson joins a group only when it matches every member")
	}
	groups := map[string]*ReflectionView{"k": {Status: "candidate"}}
	if reflectionGroup(groups, map[string][]string{"k": {a, b}}, []string{"k"}, "candidate", c) == "k" {
		t.Fatal("the reflection list must use the same rule")
	}
}

// Lessons written from a runtime's usage report differ only in status and code, so they match exactly.
func TestUsageLessonsMatchOnlyExactly(t *testing.T) {
	failed := "Skill etsy@1 ended failure with title_too_long" + usageLessonEnd
	if !sameLesson(failed, failed) || sameLesson(failed, "Skill etsy@1 ended validation_failed with title_too_long"+usageLessonEnd) {
		t.Fatal("usage lessons must match only when identical")
	}
}
