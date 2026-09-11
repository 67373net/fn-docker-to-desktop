package desktop

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
)

var magicPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// icoHeader represents the ICO file header (6 bytes)
type icoHeader struct {
	Reserved uint16 // Must be 0
	Type     uint16 // 1 = ICO, 2 = CUR
	Count    uint16 // Number of images
}

// icoEntry represents an ICO directory entry (16 bytes)
type icoEntry struct {
	Width      uint8  // Width in pixels (0 means 256)
	Height     uint8  // Height in pixels (0 means 256)
	ColorCount uint8  // Number of colors in palette (0 if >= 256 colors)
	Reserved   uint8  // Reserved, should be 0
	Planes     uint16 // Color planes (ICO) or hotspot X (CUR)
	BitCount   uint16 // Bits per pixel (ICO) or hotspot Y (CUR)
	Size       uint32 // Size of image data in bytes
	Offset     uint32 // Offset of image data from beginning of file
}

func (e *icoEntry) getActualWidth() int {
	if e.Width == 0 {
		return 256
	}
	return int(e.Width)
}

func (e *icoEntry) getActualHeight() int {
	if e.Height == 0 {
		return 256
	}
	return int(e.Height)
}

func (e *icoEntry) resolution() int {
	return e.getActualWidth() * e.getActualHeight()
}

// decodeICO decodes an ICO file and returns the highest resolution image.
// It supports both PNG and BMP encoded images within the ICO container.
func decodeICO(data []byte) (image.Image, error) {
	if len(data) < 6 {
		return nil, fmt.Errorf("invalid ICO file: too short for header")
	}

	header := icoHeader{
		Reserved: binary.LittleEndian.Uint16(data[0:2]),
		Type:     binary.LittleEndian.Uint16(data[2:4]),
		Count:    binary.LittleEndian.Uint16(data[4:6]),
	}

	if header.Reserved != 0 {
		return nil, fmt.Errorf("invalid ICO file: reserved field must be 0, got %d", header.Reserved)
	}
	if header.Type != 1 {
		return nil, fmt.Errorf("invalid ICO file: type must be 1 for ICO, got %d", header.Type)
	}
	if header.Count == 0 {
		return nil, fmt.Errorf("invalid ICO file: no images in file")
	}

	directorySize := 6 + int(header.Count)*16
	if len(data) < directorySize {
		return nil, fmt.Errorf("invalid ICO file: too short for directory entries")
	}

	var bestEntry *icoEntry
	var bestResolution int

	for i := uint16(0); i < header.Count; i++ {
		offset := 6 + int(i)*16
		entry := parseICOEntry(data[offset : offset+16])

		if entry.Offset == 0 || entry.Size == 0 {
			continue
		}
		if int(entry.Offset)+int(entry.Size) > len(data) {
			continue
		}

		resolution := entry.resolution()
		if bestEntry == nil || resolution > bestResolution {
			bestEntry = entry
			bestResolution = resolution
		}
	}

	if bestEntry == nil {
		return nil, fmt.Errorf("invalid ICO file: no valid image entries found")
	}

	imageData := data[bestEntry.Offset : bestEntry.Offset+bestEntry.Size]
	return decodeICOImage(imageData, bestEntry)
}

func parseICOEntry(data []byte) *icoEntry {
	return &icoEntry{
		Width:      data[0],
		Height:     data[1],
		ColorCount: data[2],
		Reserved:   data[3],
		Planes:     binary.LittleEndian.Uint16(data[4:6]),
		BitCount:   binary.LittleEndian.Uint16(data[6:8]),
		Size:       binary.LittleEndian.Uint32(data[8:12]),
		Offset:     binary.LittleEndian.Uint32(data[12:16]),
	}
}

func decodeICOImage(data []byte, entry *icoEntry) (image.Image, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("invalid ICO image data: too short")
	}

	if bytes.HasPrefix(data, magicPNG) {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("failed to decode PNG in ICO: %w", err)
		}
		return img, nil
	}

	return decodeICOBMP(data, entry)
}

func decodeICOBMP(data []byte, entry *icoEntry) (image.Image, error) {
	if len(data) < 40 {
		return nil, fmt.Errorf("invalid ICO BMP data: too short for header")
	}

	headerSize := binary.LittleEndian.Uint32(data[0:4])
	if headerSize < 40 {
		return nil, fmt.Errorf("invalid ICO BMP: unsupported header size %d", headerSize)
	}

	width := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	height := int(int32(binary.LittleEndian.Uint32(data[8:12])))
	bitCount := binary.LittleEndian.Uint16(data[14:16])
	compression := binary.LittleEndian.Uint32(data[16:20])

	height = height / 2

	if width == 0 {
		width = entry.getActualWidth()
	}
	if height == 0 {
		height = entry.getActualHeight()
	}

	if compression != 0 {
		return nil, fmt.Errorf("invalid ICO BMP: compressed BMP not supported (compression=%d)", compression)
	}

	pixelOffset := int(headerSize)
	var palette []uint8
	if bitCount <= 8 {
		paletteSize := 1 << bitCount
		if entry.ColorCount > 0 && int(entry.ColorCount) < paletteSize {
			paletteSize = int(entry.ColorCount)
		}
		paletteBytes := paletteSize * 4
		if len(data) < pixelOffset+paletteBytes {
			return nil, fmt.Errorf("invalid ICO BMP: too short for palette")
		}
		palette = data[pixelOffset : pixelOffset+paletteBytes]
		pixelOffset += paletteBytes
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))

	var err error
	switch bitCount {
	case 1:
		err = decodeICOBMP1(img, data[pixelOffset:], width, height, palette)
	case 4:
		err = decodeICOBMP4(img, data[pixelOffset:], width, height, palette)
	case 8:
		err = decodeICOBMP8(img, data[pixelOffset:], width, height, palette)
	case 24:
		err = decodeICOBMP24(img, data[pixelOffset:], width, height)
	case 32:
		err = decodeICOBMP32(img, data[pixelOffset:], width, height)
	default:
		return nil, fmt.Errorf("invalid ICO BMP: unsupported bit depth %d", bitCount)
	}

	if err != nil {
		return nil, err
	}

	return img, nil
}

func decodeICOBMP1(img *image.RGBA, data []byte, width, height int, palette []uint8) error {
	rowSize := ((width + 31) / 32) * 4

	for y := 0; y < height; y++ {
		srcY := height - 1 - y
		rowOffset := srcY * rowSize

		if rowOffset+rowSize > len(data) {
			break
		}

		for x := 0; x < width; x++ {
			byteIdx := x / 8
			bitIdx := 7 - (x % 8)
			pixelValue := (data[rowOffset+byteIdx] >> bitIdx) & 1

			paletteIdx := int(pixelValue) * 4
			if paletteIdx+3 < len(palette) {
				img.SetRGBA(x, y, color.RGBA{
					R: palette[paletteIdx+2],
					G: palette[paletteIdx+1],
					B: palette[paletteIdx],
					A: 255,
				})
			}
		}
	}
	return nil
}

func decodeICOBMP4(img *image.RGBA, data []byte, width, height int, palette []uint8) error {
	rowSize := ((width*4 + 31) / 32) * 4

	for y := 0; y < height; y++ {
		srcY := height - 1 - y
		rowOffset := srcY * rowSize

		if rowOffset+rowSize > len(data) {
			break
		}

		for x := 0; x < width; x++ {
			byteIdx := x / 2
			var pixelValue uint8
			if x%2 == 0 {
				pixelValue = (data[rowOffset+byteIdx] >> 4) & 0x0F
			} else {
				pixelValue = data[rowOffset+byteIdx] & 0x0F
			}

			paletteIdx := int(pixelValue) * 4
			if paletteIdx+3 < len(palette) {
				img.SetRGBA(x, y, color.RGBA{
					R: palette[paletteIdx+2],
					G: palette[paletteIdx+1],
					B: palette[paletteIdx],
					A: 255,
				})
			}
		}
	}
	return nil
}

func decodeICOBMP8(img *image.RGBA, data []byte, width, height int, palette []uint8) error {
	rowSize := ((width + 3) / 4) * 4

	for y := 0; y < height; y++ {
		srcY := height - 1 - y
		rowOffset := srcY * rowSize

		if rowOffset+width > len(data) {
			break
		}

		for x := 0; x < width; x++ {
			pixelValue := data[rowOffset+x]
			paletteIdx := int(pixelValue) * 4
			if paletteIdx+3 < len(palette) {
				img.SetRGBA(x, y, color.RGBA{
					R: palette[paletteIdx+2],
					G: palette[paletteIdx+1],
					B: palette[paletteIdx],
					A: 255,
				})
			}
		}
	}
	return nil
}

func decodeICOBMP24(img *image.RGBA, data []byte, width, height int) error {
	rowSize := ((width*3 + 3) / 4) * 4

	for y := 0; y < height; y++ {
		srcY := height - 1 - y
		rowOffset := srcY * rowSize

		if rowOffset+width*3 > len(data) {
			break
		}

		for x := 0; x < width; x++ {
			pixelOffset := rowOffset + x*3
			img.SetRGBA(x, y, color.RGBA{
				B: data[pixelOffset],
				G: data[pixelOffset+1],
				R: data[pixelOffset+2],
				A: 255,
			})
		}
	}
	return nil
}

func decodeICOBMP32(img *image.RGBA, data []byte, width, height int) error {
	rowSize := width * 4

	for y := 0; y < height; y++ {
		srcY := height - 1 - y
		rowOffset := srcY * rowSize

		if rowOffset+width*4 > len(data) {
			break
		}

		for x := 0; x < width; x++ {
			pixelOffset := rowOffset + x*4
			img.SetRGBA(x, y, color.RGBA{
				B: data[pixelOffset],
				G: data[pixelOffset+1],
				R: data[pixelOffset+2],
				A: data[pixelOffset+3],
			})
		}
	}
	return nil
}
