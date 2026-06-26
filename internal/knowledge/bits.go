package knowledge

import (
	"encoding/json"
	"math"
)

func mathFloat32bits(f float32) uint32 { return math.Float32bits(f) }
func mathFloat32frombits(u uint32) float32 { return math.Float32frombits(u) }

func jsonDecodeString(s string, dst any) error {
	return json.Unmarshal([]byte(s), dst)
}
