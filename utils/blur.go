package utils

import (
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
)

// LaplacianBlurScore calculates sharpness of an image.
// Score < 100  → blurry
// Score 100–300 → acceptable
// Score > 300  → clear
func LaplacianBlurScore(filePath string) (float64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 999, err // can't open → assume fine, don't block
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return 999, err // not an image (e.g. PDF) → skip blur check
	}

	bounds := img.Bounds()
	width := bounds.Max.X
	height := bounds.Max.Y

	// Laplacian kernel: [0,1,0 / 1,-4,1 / 0,1,0]
	var sum, sumSq float64
	var count float64

	toGray := func(x, y int) float64 {
		c := img.At(x, y)
		gray, _, _, _ := color.GrayModel.Convert(c).RGBA()
		return float64(gray >> 8)
	}

	for y := 1; y < height-1; y++ {
		for x := 1; x < width-1; x++ {
			lap := -4*toGray(x, y) +
				toGray(x-1, y) + toGray(x+1, y) +
				toGray(x, y-1) + toGray(x, y+1)
			sum += lap
			sumSq += lap * lap
			count++
		}
	}

	if count == 0 {
		return 999, nil
	}

	mean := sum / count
	variance := (sumSq / count) - (mean * mean)
	return math.Abs(variance), nil
}

// IsBlurry returns true if the image sharpness score is below threshold
func IsBlurry(filePath string) (bool, float64) {
	score, err := LaplacianBlurScore(filePath)
	if err != nil {
		return false, 999 // can't check → assume fine
	}
	return score < 100, score
}
