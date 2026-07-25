//go:build im_wx && !im_qq

package im

// OptionalPlatformTags returns high-risk IM platforms compiled into this binary.
func OptionalPlatformTags() []string {
	return []string{"wechat"}
}
