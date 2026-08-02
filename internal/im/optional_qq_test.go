//go:build im_qq && !im_wx

package im

import "testing"

func TestOptionalPlatformTagsQQ(t *testing.T) {
	tags := OptionalPlatformTags()
	if len(tags) != 1 || tags[0] != "qq" {
		t.Fatalf("tags = %v, want [qq]", tags)
	}
}
