//go:build im_qq && im_wx

package im

// OptionalPlatformTags returns high-risk IM platforms compiled into this binary.
func OptionalPlatformTags() []string {
	return []string{"qq", "wechat"}
}
