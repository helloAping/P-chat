package export

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type pdfUnicodeFont struct {
	name    string
	raw     []byte
	metrics pdfFontMetrics
	glyphs  map[rune]uint16
	widths  map[rune]int
	used    []rune
}

type pdfFontMetrics struct {
	ascent    int
	descent   int
	capHeight int
	bbox      [4]int
}

type ttfTable struct {
	offset uint32
	length uint32
}

func buildPDFWithEmbeddedFont(pageContents []string, font *pdfUnicodeFont) []byte {
	var objects [][]byte
	addBytes := func(b []byte) int {
		objects = append(objects, b)
		return len(objects)
	}
	add := func(s string) int {
		return addBytes([]byte(s))
	}

	catalogID := add("")
	pagesID := add("")
	fontFileID := addBytes(pdfStream(fmt.Sprintf("/Length1 %d", len(font.raw)), font.raw))
	descriptorID := add(fmt.Sprintf(
		"<< /Type /FontDescriptor /FontName /%s /Flags 4 /FontBBox [%d %d %d %d] /ItalicAngle 0 /Ascent %d /Descent %d /CapHeight %d /StemV 80 /FontFile2 %d 0 R >>",
		font.name,
		font.metrics.bbox[0], font.metrics.bbox[1], font.metrics.bbox[2], font.metrics.bbox[3],
		font.metrics.ascent, font.metrics.descent, font.metrics.capHeight,
		fontFileID,
	))
	cidToGIDID := addBytes(pdfStream("", buildCIDToGIDMap(font)))
	toUnicodeID := addBytes(pdfStream("", []byte(buildToUnicodeCMap(font))))
	cidFontID := add(fmt.Sprintf(
		"<< /Type /Font /Subtype /CIDFontType2 /BaseFont /%s /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor %d 0 R /CIDToGIDMap %d 0 R /DW 500 /W %s >>",
		font.name, descriptorID, cidToGIDID, buildPDFWidthArray(font),
	))
	fontID := add(fmt.Sprintf(
		"<< /Type /Font /Subtype /Type0 /BaseFont /%s /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>",
		font.name, cidFontID, toUnicodeID,
	))
	_ = catalogID
	_ = pagesID
	_ = fontID

	pageIDs := make([]int, 0, len(pageContents))
	for _, content := range pageContents {
		contentBytes := []byte(content)
		contentID := addBytes(pdfStream("", contentBytes))
		pageID := add(fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %.0f %.0f] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			pagesID, pdfPageW, pdfPageH, fontID, contentID))
		pageIDs = append(pageIDs, pageID)
	}

	var kids strings.Builder
	for _, id := range pageIDs {
		fmt.Fprintf(&kids, "%d 0 R ", id)
	}
	objects[catalogID-1] = []byte(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesID))
	objects[pagesID-1] = []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids.String(), len(pageIDs)))

	return writePDFObjects(objects, catalogID)
}

func loadPDFUnicodeFont(used map[rune]struct{}) (*pdfUnicodeFont, error) {
	needed := make(map[rune]struct{}, len(used)+2)
	for rr := range used {
		needed[rr] = struct{}{}
	}
	needed[' '] = struct{}{}
	needed['?'] = struct{}{}

	var errs []error
	for _, path := range pdfFontCandidates() {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		font, err := parsePDFTrueTypeFont(raw, needed)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			continue
		}
		font.name = "PChatUnicode"
		return font, nil
	}
	if len(errs) == 0 {
		return nil, errors.New("no usable system TrueType font found")
	}
	return nil, errors.Join(errs...)
}

func pdfFontCandidates() []string {
	var out []string
	addWindowsFonts := func(root string) {
		if root == "" {
			return
		}
		fonts := filepath.Join(root, "Fonts")
		out = append(out,
			filepath.Join(fonts, "NotoSansSC-VF.ttf"),
			filepath.Join(fonts, "NotoSerifSC-VF.ttf"),
			filepath.Join(fonts, "msyh.ttc"),
			filepath.Join(fonts, "msyhbd.ttc"),
			filepath.Join(fonts, "simhei.ttf"),
			filepath.Join(fonts, "simsunb.ttf"),
			filepath.Join(fonts, "Deng.ttf"),
		)
	}
	addWindowsFonts(os.Getenv("WINDIR"))
	addWindowsFonts(`C:\Windows`)
	out = append(out,
		"/System/Library/Fonts/PingFang.ttc",
		"/System/Library/Fonts/STHeiti Light.ttc",
		"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
		"/usr/share/fonts/truetype/noto/NotoSansCJK-Regular.ttc",
		"/usr/share/fonts/truetype/wqy/wqy-microhei.ttc",
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	)
	return dedupeStrings(out)
}

func parsePDFTrueTypeFont(raw []byte, used map[rune]struct{}) (*pdfUnicodeFont, error) {
	tables, err := parseTTFTables(raw)
	if err != nil {
		return nil, err
	}
	head, err := tableBytes(raw, tables, "head")
	if err != nil {
		return nil, err
	}
	hhea, err := tableBytes(raw, tables, "hhea")
	if err != nil {
		return nil, err
	}
	hmtx, err := tableBytes(raw, tables, "hmtx")
	if err != nil {
		return nil, err
	}
	maxp, err := tableBytes(raw, tables, "maxp")
	if err != nil {
		return nil, err
	}
	cmap, err := tableBytes(raw, tables, "cmap")
	if err != nil {
		return nil, err
	}

	if len(head) < 54 || len(hhea) < 36 || len(maxp) < 6 {
		return nil, errors.New("truncated TrueType metrics")
	}
	unitsPerEm := int(binary.BigEndian.Uint16(head[18:20]))
	if unitsPerEm <= 0 {
		unitsPerEm = 1000
	}
	numGlyphs := int(binary.BigEndian.Uint16(maxp[4:6]))
	numHMetrics := int(binary.BigEndian.Uint16(hhea[34:36]))
	if numHMetrics <= 0 || len(hmtx) < 4 {
		return nil, errors.New("invalid hmtx metrics")
	}

	glyphs := parsePDFCMap(cmap, used)
	if missing := missingCriticalFontRunes(used, glyphs); len(missing) > 0 {
		return nil, fmt.Errorf("font cmap missing requested text: %U", missing[0])
	}
	fallbackGID := glyphs['?']
	if fallbackGID == 0 {
		fallbackGID = glyphs[' ']
	}

	advanceWidth := func(gid uint16) int {
		g := int(gid)
		if g >= numGlyphs {
			return 500
		}
		if g < numHMetrics {
			pos := g * 4
			if pos+2 <= len(hmtx) {
				return int(binary.BigEndian.Uint16(hmtx[pos : pos+2]))
			}
			return 500
		}
		pos := (numHMetrics - 1) * 4
		if pos+2 <= len(hmtx) {
			return int(binary.BigEndian.Uint16(hmtx[pos : pos+2]))
		}
		return 500
	}

	usedRunes := sortedRunes(used)
	widths := make(map[rune]int, len(usedRunes))
	for _, rr := range usedRunes {
		gid := glyphs[rr]
		if gid == 0 && fallbackGID != 0 {
			gid = fallbackGID
			glyphs[rr] = fallbackGID
		}
		w := scaleFontMetric(advanceWidth(gid), unitsPerEm)
		if w <= 0 {
			w = 500
		}
		widths[rr] = w
	}

	xMin := int(int16(binary.BigEndian.Uint16(head[36:38])))
	yMin := int(int16(binary.BigEndian.Uint16(head[38:40])))
	xMax := int(int16(binary.BigEndian.Uint16(head[40:42])))
	yMax := int(int16(binary.BigEndian.Uint16(head[42:44])))
	ascent := int(int16(binary.BigEndian.Uint16(hhea[4:6])))
	descent := int(int16(binary.BigEndian.Uint16(hhea[6:8])))

	return &pdfUnicodeFont{
		raw:    raw,
		glyphs: glyphs,
		widths: widths,
		used:   usedRunes,
		metrics: pdfFontMetrics{
			ascent:    scaleFontMetric(ascent, unitsPerEm),
			descent:   scaleFontMetric(descent, unitsPerEm),
			capHeight: scaleFontMetric(ascent, unitsPerEm),
			bbox: [4]int{
				scaleFontMetric(xMin, unitsPerEm),
				scaleFontMetric(yMin, unitsPerEm),
				scaleFontMetric(xMax, unitsPerEm),
				scaleFontMetric(yMax, unitsPerEm),
			},
		},
	}, nil
}

func missingCriticalFontRunes(used map[rune]struct{}, glyphs map[rune]uint16) []rune {
	var missing []rune
	for rr := range used {
		if rr < 0 || rr > 0xffff {
			continue
		}
		if rr == '\r' || rr == '\n' || rr == '\t' {
			continue
		}
		if !isCriticalPDFRune(rr) {
			continue
		}
		if _, ok := glyphs[rr]; !ok {
			missing = append(missing, rr)
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		return missing[i] < missing[j]
	})
	return missing
}

func isCriticalPDFRune(rr rune) bool {
	if rr >= 0x20 && rr <= 0x7e {
		return true
	}
	return (rr >= 0x3400 && rr <= 0x9fff) ||
		(rr >= 0xf900 && rr <= 0xfaff)
}

func parseTTFTables(raw []byte) (map[string]ttfTable, error) {
	if len(raw) < 12 {
		return nil, errors.New("truncated TrueType header")
	}
	tag := string(raw[:4])
	if tag == "ttcf" {
		return nil, errors.New("TrueType collection is not supported")
	}
	if tag == "OTTO" {
		return nil, errors.New("OpenType CFF fonts are not supported")
	}
	numTables := int(binary.BigEndian.Uint16(raw[4:6]))
	if 12+numTables*16 > len(raw) {
		return nil, errors.New("truncated TrueType table directory")
	}
	tables := make(map[string]ttfTable, numTables)
	for i := 0; i < numTables; i++ {
		pos := 12 + i*16
		name := string(raw[pos : pos+4])
		offset := binary.BigEndian.Uint32(raw[pos+8 : pos+12])
		length := binary.BigEndian.Uint32(raw[pos+12 : pos+16])
		if uint64(offset)+uint64(length) > uint64(len(raw)) {
			return nil, fmt.Errorf("table %s outside font bounds", name)
		}
		tables[name] = ttfTable{offset: offset, length: length}
	}
	return tables, nil
}

func tableBytes(raw []byte, tables map[string]ttfTable, tag string) ([]byte, error) {
	t, ok := tables[tag]
	if !ok {
		return nil, fmt.Errorf("missing %s table", tag)
	}
	return raw[t.offset : t.offset+t.length], nil
}

func parsePDFCMap(cmap []byte, used map[rune]struct{}) map[rune]uint16 {
	if len(cmap) < 4 {
		return nil
	}
	type subtable struct {
		offset int
		format uint16
		score  int
	}
	count := int(binary.BigEndian.Uint16(cmap[2:4]))
	var subtables []subtable
	for i := 0; i < count; i++ {
		pos := 4 + i*8
		if pos+8 > len(cmap) {
			break
		}
		platform := binary.BigEndian.Uint16(cmap[pos : pos+2])
		encoding := binary.BigEndian.Uint16(cmap[pos+2 : pos+4])
		offset := int(binary.BigEndian.Uint32(cmap[pos+4 : pos+8]))
		if offset+2 > len(cmap) {
			continue
		}
		format := binary.BigEndian.Uint16(cmap[offset : offset+2])
		score := 0
		switch {
		case format == 12 && platform == 3 && encoding == 10:
			score = 100
		case format == 12:
			score = 90
		case format == 4 && platform == 3:
			score = 80
		case format == 4:
			score = 70
		}
		if score > 0 {
			subtables = append(subtables, subtable{offset: offset, format: format, score: score})
		}
	}
	sort.Slice(subtables, func(i, j int) bool {
		return subtables[i].score > subtables[j].score
	})

	out := make(map[rune]uint16, len(used))
	for _, st := range subtables {
		var got map[rune]uint16
		switch st.format {
		case 4:
			got = parsePDFCMapFormat4(cmap[st.offset:], used)
		case 12:
			got = parsePDFCMapFormat12(cmap[st.offset:], used)
		}
		for rr, gid := range got {
			if gid == 0 {
				continue
			}
			if _, ok := out[rr]; !ok {
				out[rr] = gid
			}
		}
		if len(out) == len(used) {
			break
		}
	}
	return out
}

func parsePDFCMapFormat4(st []byte, used map[rune]struct{}) map[rune]uint16 {
	if len(st) < 16 {
		return nil
	}
	length := int(binary.BigEndian.Uint16(st[2:4]))
	if length <= 0 || length > len(st) {
		length = len(st)
	}
	st = st[:length]
	segCount := int(binary.BigEndian.Uint16(st[6:8]) / 2)
	endCodeOff := 14
	startCodeOff := endCodeOff + segCount*2 + 2
	deltaOff := startCodeOff + segCount*2
	rangeOff := deltaOff + segCount*2
	if rangeOff+segCount*2 > len(st) {
		return nil
	}

	out := make(map[rune]uint16, len(used))
	for rr := range used {
		if rr < 0 || rr > 0xffff {
			continue
		}
		c := uint16(rr)
		for i := 0; i < segCount; i++ {
			end := binary.BigEndian.Uint16(st[endCodeOff+i*2 : endCodeOff+i*2+2])
			start := binary.BigEndian.Uint16(st[startCodeOff+i*2 : startCodeOff+i*2+2])
			if c < start || c > end {
				continue
			}
			delta := int16(binary.BigEndian.Uint16(st[deltaOff+i*2 : deltaOff+i*2+2]))
			rangePos := rangeOff + i*2
			rangeOffset := binary.BigEndian.Uint16(st[rangePos : rangePos+2])
			var gid uint16
			if rangeOffset == 0 {
				gid = uint16(int(c) + int(delta))
			} else {
				glyphPos := rangePos + int(rangeOffset) + int(c-start)*2
				if glyphPos+2 > len(st) {
					break
				}
				gid = binary.BigEndian.Uint16(st[glyphPos : glyphPos+2])
				if gid != 0 {
					gid = uint16(int(gid) + int(delta))
				}
			}
			if gid != 0 {
				out[rr] = gid
			}
			break
		}
	}
	return out
}

func parsePDFCMapFormat12(st []byte, used map[rune]struct{}) map[rune]uint16 {
	if len(st) < 16 {
		return nil
	}
	length := int(binary.BigEndian.Uint32(st[4:8]))
	if length <= 0 || length > len(st) {
		length = len(st)
	}
	st = st[:length]
	groups := int(binary.BigEndian.Uint32(st[12:16]))
	if 16+groups*12 > len(st) {
		return nil
	}
	out := make(map[rune]uint16, len(used))
	for rr := range used {
		if rr < 0 || rr > 0xffff {
			continue
		}
		c := uint32(rr)
		for i := 0; i < groups; i++ {
			pos := 16 + i*12
			start := binary.BigEndian.Uint32(st[pos : pos+4])
			end := binary.BigEndian.Uint32(st[pos+4 : pos+8])
			if c < start || c > end {
				continue
			}
			gid := binary.BigEndian.Uint32(st[pos+8:pos+12]) + c - start
			if gid <= 0xffff {
				out[rr] = uint16(gid)
			}
			break
		}
	}
	return out
}

func buildCIDToGIDMap(font *pdfUnicodeFont) []byte {
	maxCID := rune(0)
	for _, rr := range font.used {
		if rr > maxCID {
			maxCID = rr
		}
	}
	data := make([]byte, (int(maxCID)+1)*2)
	for _, rr := range font.used {
		gid := font.glyphs[rr]
		pos := int(rr) * 2
		binary.BigEndian.PutUint16(data[pos:pos+2], gid)
	}
	return data
}

func buildPDFWidthArray(font *pdfUnicodeFont) string {
	var b strings.Builder
	b.WriteByte('[')
	for _, rr := range font.used {
		w := font.widths[rr]
		if w <= 0 {
			w = 500
		}
		fmt.Fprintf(&b, "%d [%d] ", rr, w)
	}
	b.WriteByte(']')
	return b.String()
}

func buildToUnicodeCMap(font *pdfUnicodeFont) string {
	var b strings.Builder
	b.WriteString("/CIDInit /ProcSet findresource begin\n")
	b.WriteString("12 dict begin\nbegincmap\n")
	b.WriteString("/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n")
	b.WriteString("/CMapName /PChatToUnicode def\n/CMapType 2 def\n")
	b.WriteString("1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n")
	for start := 0; start < len(font.used); start += 100 {
		end := start + 100
		if end > len(font.used) {
			end = len(font.used)
		}
		fmt.Fprintf(&b, "%d beginbfchar\n", end-start)
		for _, rr := range font.used[start:end] {
			fmt.Fprintf(&b, "<%04X> <%04X>\n", rr, rr)
		}
		b.WriteString("endbfchar\n")
	}
	b.WriteString("endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n")
	return b.String()
}

func pdfStream(dictEntries string, data []byte) []byte {
	var b bytes.Buffer
	dictEntries = strings.TrimSpace(dictEntries)
	if dictEntries == "" {
		fmt.Fprintf(&b, "<< /Length %d >>\nstream\n", len(data))
	} else {
		fmt.Fprintf(&b, "<< %s /Length %d >>\nstream\n", dictEntries, len(data))
	}
	b.Write(data)
	b.WriteString("\nendstream")
	return b.Bytes()
}

func sortedRunes(in map[rune]struct{}) []rune {
	out := make([]rune, 0, len(in))
	for rr := range in {
		if rr >= 0 && rr <= 0xffff {
			out = append(out, rr)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}

func scaleFontMetric(v, unitsPerEm int) int {
	if unitsPerEm <= 0 {
		return v
	}
	if v >= 0 {
		return (v*1000 + unitsPerEm/2) / unitsPerEm
	}
	return -((-v*1000 + unitsPerEm/2) / unitsPerEm)
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
