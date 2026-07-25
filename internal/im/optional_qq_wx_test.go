//go:build im_qq && im_wx

package im

import "testing"

func TestOptionalPlatformTagsQQWX(t *testing.T) {
	tags := OptionalPlatformTags()
	if len(tags) != 2 || tags[0] != "qq" || tags[1] != "wechat" {
		t.Fatalf("tags = %v, want [qq wechat]", tags)
	}
}
