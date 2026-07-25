//go:build !im_qq && !im_wx

package im

import "testing"

func TestOptionalPlatformTagsDefault(t *testing.T) {
	tags := OptionalPlatformTags()
	for _, tag := range tags {
		if tag == "qq" || tag == "wechat" {
			t.Fatalf("default build should not include high-risk optional platform %q", tag)
		}
	}
}
