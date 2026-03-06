package tray

// iconOn - зелёная иконка (прокси включён)
// iconOff - серая иконка (прокси выключен)
// Минимальные 16x16 ICO файлы, закодированные в []byte

// generateIcon создаёт простую 16x16 иконку нужного цвета в формате ICO
func iconOn() []byte {
	return buildIcon(0x00, 0xE6, 0x76) // зелёный #00e676
}

func iconOff() []byte {
	return buildIcon(0x4A, 0x4A, 0x6A) // серый #4a4a6a
}

// buildIcon строит минимальный ICO 16x16 с одним цветом
func buildIcon(r, g, b byte) []byte {
	// ICO header (6 bytes)
	// Image directory entry (16 bytes)
	// DIB header BITMAPINFOHEADER (40 bytes)
	// Pixel data 16x16 BGRA (1024 bytes)
	// XOR mask + AND mask

	const w, h = 16, 16
	pixelData := make([]byte, w*h*4)
	for i := 0; i < w*h; i++ {
		pixelData[i*4+0] = b // Blue
		pixelData[i*4+1] = g // Green
		pixelData[i*4+2] = r // Red
		pixelData[i*4+3] = 0xFF // Alpha
	}

	// Нарисуем круг в центре (остальное прозрачное)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx := float64(x) - 7.5
			dy := float64(y) - 7.5
			if dx*dx+dy*dy > 49 { // радиус 7
				pixelData[(y*w+x)*4+3] = 0x00 // прозрачный
			}
		}
	}

	dibSize := 40 + len(pixelData) + w*h/8 // header + pixels + AND mask
	icoSize := 6 + 16 + dibSize

	ico := make([]byte, icoSize)
	off := 0

	// ICO header
	ico[off+0] = 0x00 // reserved
	ico[off+1] = 0x00
	ico[off+2] = 0x01 // type: ICO
	ico[off+3] = 0x00
	ico[off+4] = 0x01 // count: 1 image
	ico[off+5] = 0x00
	off += 6

	// Directory entry
	ico[off+0] = w        // width
	ico[off+1] = h        // height
	ico[off+2] = 0x00     // color count
	ico[off+3] = 0x00     // reserved
	ico[off+4] = 0x01     // planes
	ico[off+5] = 0x00
	ico[off+6] = 0x20     // bits per pixel (32)
	ico[off+7] = 0x00
	putU32(ico, off+8, uint32(dibSize))
	putU32(ico, off+12, 22) // offset to image data
	off += 16

	// BITMAPINFOHEADER
	putU32(ico, off+0, 40)          // header size
	putU32(ico, off+4, uint32(w))   // width
	putU32(ico, off+8, uint32(h*2)) // height * 2 (XOR + AND)
	ico[off+12] = 0x01               // planes
	ico[off+13] = 0x00
	ico[off+14] = 0x20 // bpp = 32
	ico[off+15] = 0x00
	// rest zeros (compression=0, etc.)
	off += 40

	// Pixel data (bottom-up)
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			src := (y*w + x) * 4
			ico[off+0] = pixelData[src+0]
			ico[off+1] = pixelData[src+1]
			ico[off+2] = pixelData[src+2]
			ico[off+3] = pixelData[src+3]
			off += 4
		}
	}

	// AND mask (all zeros = fully visible)
	for i := 0; i < w*h/8; i++ {
		ico[off+i] = 0x00
	}

	return ico
}

func putU32(b []byte, off int, v uint32) {
	b[off+0] = byte(v)
	b[off+1] = byte(v >> 8)
	b[off+2] = byte(v >> 16)
	b[off+3] = byte(v >> 24)
}
