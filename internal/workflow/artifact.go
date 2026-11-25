package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// StepWorkID identifies the work directory for one immutable run step attempt.
func StepWorkID(uid string, stepName string, attempt int64) string {
	identity := uid + "\x00" + stepName
	if attempt > 1 {
		identity = fmt.Sprintf("%s\x00%d", identity, attempt)
	}

	hash := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(hash[:16])
}

// StepOutputFile returns the published filename for a supported step.
func StepOutputFile(stepType string, externalProvider bool) string {
	switch stepType {
	case "llm":
		return "script.json"
	case "video":
		return "clip.mp4"
	case "ffmpeg":
		return "output.mp4"
	case "image":
		if externalProvider {
			return "image.png"
		}
		return "scene.svg"
	default:
		return "scene.svg"
	}
}
