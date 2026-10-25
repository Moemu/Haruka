// boundaries 是构建期工具：把 GeoBoundaries 的 CHN ADM1/ADM3 GeoJSON 简化、量化，
// 并用 DataV GeoAtlas 的区划名称表（名称/代码/驻地坐标，事实数据）按「驻地落点匹配」
// 挂上中文名，合并为 Android 端离线反解用的边界包（assets 内 gzip 存储）。
//
// 为什么这样组合：GeoBoundaries CHN 的 shapeName 是英文/拼音且 ADM2 实为县级
// （无地市图层），协议需要中文行政区名；区划名称与代码是公开事实数据（GB/T 2260），
// 几何采用 GeoBoundaries（ODbL 1.0，© OpenStreetMap contributors，经 geoBoundaries
// 转载），派生数据需保留同许可与署名（THIRD_PARTY_NOTICES.md）。
//
// 用法：
//
//	go build -o bin/boundaries ./cmd/boundaries
//	bin/boundaries -adm1 a1.geojson -adm3 a3.geojson -datav <datav目录> -out haruka-boundaries.json
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ring 是简化后的环（不含重复收尾点），坐标已量化。
type ring []float64 // [x0,y0,x1,y1,...]

type outArea struct {
	Name  string    `json:"n"`
	BBox  []float64 `json:"b"` // [minLon, minLat, maxLon, maxLat]（度）
	Rings []ring    `json:"r"`
	City  string    `json:"c,omitempty"` // 仅 adm3：所属地市中文名
	Prov  string    `json:"p,omitempty"` // 仅 adm3：所属省级中文名
}

type outBundle struct {
	ADM1 []outArea `json:"adm1"`
	ADM3 []outArea `json:"adm3"`
}

type geoFeature struct {
	Properties struct {
		ShapeName string `json:"shapeName"`
	} `json:"properties"`
	Geometry struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
}

type geoJSON struct {
	Features []geoFeature `json:"features"`
}

// datavEntry 是 DataV 区划表里的一条事实记录。
type datavEntry struct {
	Name    string
	Level   string
	Parent  int64
	CenterX float64
	CenterY float64
}

// flexInt 兼容 DataV 各文件里 adcode 的数字/字符串混合形态。
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		// "100000_JD" 之类的特殊要素：置 0，由调用方守卫跳过
		*f = 0
		return nil
	}
	*f = flexInt(v)
	return nil
}

func main() {
	adm1 := flag.String("adm1", "", "GeoBoundaries ADM1 GeoJSON 路径")
	adm3 := flag.String("adm3", "", "GeoBoundaries ADM3 GeoJSON 路径")
	datavDir := flag.String("datav", "", "DataV 区划表目录（p*.json / c*.json）")
	out := flag.String("out", "haruka-boundaries.json", "输出路径")
	tol := flag.Float64("tol", 0.005, "Douglas-Peucker 容差（度，0.005≈500m）")
	quant := flag.Float64("quant", 0.001, "坐标量化步长（度，0.001≈100m）")
	flag.Parse()
	if *adm1 == "" || *adm3 == "" || *datavDir == "" {
		fmt.Fprintln(os.Stderr, "需要 -adm1 -adm3 -datav")
		os.Exit(2)
	}

	provinces, cities, districts := loadDatav(*datavDir)
	fmt.Printf("区划表: 省 %d / 市 %d / 县 %d\n", len(provinces), len(cities), len(districts))

	// ADM1：挂省级中文名
	adm1Areas, unmatched1 := processFile(*adm1, *tol, *quant, func(rings []ring) (outArea, bool) {
		for _, p := range provinces {
			if pointInRings(p.CenterX, p.CenterY, rings) {
				return outArea{Name: p.Name}, true
			}
		}
		return outArea{}, false
	})

	// ADM3：挂 县/市/省 三级中文名（驻地落点匹配；直辖县级的市名回退省名）
	adm3Areas, unmatched3 := processFile(*adm3, *tol, *quant, func(rings []ring) (outArea, bool) {
		for _, d := range districts {
			if pointInRings(d.CenterX, d.CenterY, rings) {
				area := outArea{Name: d.Name, City: d.Name, Prov: d.Name}
				if city, ok := cities[d.Parent]; ok {
					area.City = city.Name
					if prov, ok := provinces[cityParent(cities, provinces, d.Parent)]; ok {
						area.Prov = prov.Name
					} else {
						area.Prov = city.Name // 直辖市：市即省
					}
				} else if prov, ok := provinces[d.Parent]; ok {
					// 直辖县级（济源/东莞类）：parent 即省，市名回退省名
					area.City = prov.Name
					area.Prov = prov.Name
				} else {
					return outArea{}, false
				}
				return area, true
			}
		}
		return outArea{}, false
	})

	fmt.Printf("ADM1 匹配 %d / 未匹配丢弃 %d；ADM3 匹配 %d / 未匹配丢弃 %d\n",
		len(adm1Areas), unmatched1, len(adm3Areas), unmatched3)

	bundle := outBundle{ADM1: adm1Areas, ADM3: adm3Areas}
	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(bundle); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	st, _ := os.Stat(*out)
	fmt.Printf("输出 %s（%d MB）\n", *out, st.Size()>>20)
}

// cityParent 沿市→省链取省级 adcode；市级 parent 缺失时返回 0。
func cityParent(cities, provinces map[int64]datavEntry, cityAdcode int64) int64 {
	if c, ok := cities[cityAdcode]; ok && provinces[c.Parent].Name != "" {
		return c.Parent
	}
	return 0
}

// loadDatav 扫描目录下所有 *_full.json，汇集省/市/县三级事实表。
func loadDatav(dir string) (provinces, cities map[int64]datavEntry, districts []datavEntry) {
	provinces = map[int64]datavEntry{}
	cities = map[int64]datavEntry{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(files)
	type rawProps struct {
		Name    string   `json:"name"`
		Adcode  flexInt  `json:"adcode"`
		Level   string   `json:"level"`
		Parent  struct {
			Adcode flexInt `json:"adcode"`
		} `json:"parent"`
		Center []float64 `json:"center"`
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var doc struct {
			Features []struct {
				Properties rawProps `json:"properties"`
			} `json:"features"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			fmt.Fprintf(os.Stderr, "datav 解析失败 %s: %v\n", f, err)
			continue
		}
		for _, feat := range doc.Features {
			p := feat.Properties
			if p.Name == "" || p.Adcode == 0 || len(p.Center) < 2 {
				continue
			}
			e := datavEntry{Name: p.Name, Level: p.Level, Parent: int64(p.Parent.Adcode), CenterX: p.Center[0], CenterY: p.Center[1]}
			switch p.Level {
			case "province":
				provinces[int64(p.Adcode)] = e
			case "city":
				cities[int64(p.Adcode)] = e
			case "district":
				districts = append(districts, e)
			}
		}
	}
	return provinces, cities, districts
}

// processFile 解析一层 GeoBoundaries GeoJSON：简化、量化、按 matchFn 挂名，未匹配即丢弃。
func processFile(path string, tol, quant float64, match func([]ring) (outArea, bool)) (areas []outArea, unmatched int) {
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取 %s 失败: %v\n", path, err)
		os.Exit(1)
	}
	var gj geoJSON
	if err := json.Unmarshal(raw, &gj); err != nil {
		fmt.Fprintf(os.Stderr, "解析 %s: %v\n", path, err)
		os.Exit(1)
	}
	for _, feat := range gj.Features {
		if strings.TrimSpace(feat.Properties.ShapeName) == "" {
			continue
		}
		srcRings, err := parseRings(feat.Geometry.Type, feat.Geometry.Coordinates)
		if err != nil {
			continue
		}
		rings := make([]ring, 0, len(srcRings))
		for _, r := range srcRings {
			q := quantizeRing(simplifyRing(r, tol), quant)
			if len(q) >= 4 {
				rings = append(rings, q)
			}
		}
		if len(rings) == 0 {
			continue
		}
		area, ok := match(rings)
		if !ok {
			unmatched++
			continue
		}
		area.Rings = rings
		area.BBox = bboxOf(rings, quant)
		areas = append(areas, area)
	}
	return areas, unmatched
}

// pointInRings 偶奇规则点在多边形内判定（全部环统一计交叉数）。
func pointInRings(x, y float64, rings []ring) bool {
	inside := false
	for _, r := range rings {
		j := len(r) - 2
		for i := 0; i+1 < len(r); i += 2 {
			xi, yi, xj, yj := r[i], r[i+1], r[j], r[j+1]
			if (yi > y) != (yj > y) {
				if x < (xj-xi)*(y-yi)/(yj-yi)+xi {
					inside = !inside
				}
			}
			j = i
		}
	}
	return inside
}

// parseRings 把 Polygon / MultiPolygon 统一展平为环列表（洞环保留，PIP 走偶奇规则）。
func parseRings(typ string, raw json.RawMessage) ([][][2]float64, error) {
	switch typ {
	case "Polygon":
		var poly [][][2]float64
		if err := json.Unmarshal(raw, &poly); err != nil {
			return nil, err
		}
		return poly, nil
	case "MultiPolygon":
		var multi [][][][2]float64
		if err := json.Unmarshal(raw, &multi); err != nil {
			return nil, err
		}
		var rings [][][2]float64
		for _, poly := range multi {
			rings = append(rings, poly...)
		}
		return rings, nil
	default:
		return nil, fmt.Errorf("不支持的几何类型 %q", typ)
	}
}

// simplifyRing 对闭合环做 Douglas-Peucker：以相距最远的两点为锚切环，两段分别 DP。
// 关键：先把环旋转到锚点为起点，让两条链的内部下标都是直接映射——
// 否则绕回段的 keep 下标会越界丢失，整段边界被一根弦取代。
func simplifyRing(pts [][2]float64, tol float64) [][2]float64 {
	if len(pts) > 1 && pts[0] == pts[len(pts)-1] {
		pts = pts[:len(pts)-1]
	}
	n := len(pts)
	if n < 3 {
		return pts
	}
	i0, i1 := 0, 0
	maxD := -1.0
	for i := 0; i < n; i++ {
		if d := dist2(pts[i], pts[0]); d > maxD {
			maxD, i1 = d, i
		}
	}
	for i := 0; i < n; i++ {
		if d := dist2(pts[i], pts[i1]); d > maxD {
			maxD, i0 = d, i
		}
	}
	// 旋转：锚 i0 变为下标 0，锚 i1 变为下标 m
	rotated := make([][2]float64, 0, n)
	rotated = append(rotated, pts[i0:]...)
	rotated = append(rotated, pts[:i0]...)
	m := (i1 - i0 + n) % n

	keep := map[int]bool{0: true, m: true}
	// 链 A：0 → m
	dpMark(rotated[:m+1], tol, keep, 0)
	// 链 B：m → n-1 →（回到起点）
	seg2 := append(append([][2]float64{}, rotated[m:]...), rotated[0])
	dpMark(seg2, tol, keep, m)

	out := make([][2]float64, 0, len(keep))
	for k := 0; k < n; k++ {
		if keep[k] {
			out = append(out, rotated[k])
		}
	}
	return out
}

// dpMark 递归 DP，保留点下标写入 keep（seg 的首点对应 pts 的 base 下标）。
func dpMark(pts [][2]float64, tol float64, keep map[int]bool, base int) {
	if len(pts) < 3 {
		return
	}
	maxD, idx := -1.0, -1
	a, b := pts[0], pts[len(pts)-1]
	for i := 1; i < len(pts)-1; i++ {
		d := pointSegDist2(pts[i], a, b)
		if d > maxD {
			maxD, idx = d, i
		}
	}
	if maxD <= tol*tol {
		return
	}
	keep[base+idx] = true
	dpMark(pts[:idx+1], tol, keep, base)
	dpMark(pts[idx:], tol, keep, base+idx)
}

func quantizeRing(pts [][2]float64, q float64) ring {
	r := make(ring, 0, len(pts)*2)
	var lastX, lastY float64 = math.NaN(), math.NaN()
	for _, p := range pts {
		x := math.Round(p[0]/q) * q
		y := math.Round(p[1]/q) * q
		if x == lastX && y == lastY {
			continue
		}
		r = append(r, x, y)
		lastX, lastY = x, y
	}
	if len(r) >= 4 && r[0] == r[len(r)-2] && r[1] == r[len(r)-1] {
		r = r[:len(r)-2]
	}
	return r
}

func bboxOf(rings []ring, q float64) []float64 {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, r := range rings {
		for i := 0; i+1 < len(r); i += 2 {
			minX = math.Min(minX, r[i])
			maxX = math.Max(maxX, r[i])
			minY = math.Min(minY, r[i+1])
			maxY = math.Max(maxY, r[i+1])
		}
	}
	return []float64{
		math.Floor(minX/q) * q, math.Floor(minY/q) * q,
		math.Ceil(maxX/q) * q, math.Ceil(maxY/q) * q,
	}
}

func dist2(a, b [2]float64) float64 {
	dx, dy := a[0]-b[0], a[1]-b[1]
	return dx*dx + dy*dy
}

func pointSegDist2(p, a, b [2]float64) float64 {
	abx, aby := b[0]-a[0], b[1]-a[1]
	apx, apy := p[0]-a[0], p[1]-a[1]
	ab2 := abx*abx + aby*aby
	if ab2 == 0 {
		return apx*apx + apy*apy
	}
	t := (apx*abx + apy*aby) / ab2
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	dx, dy := apx-t*abx, apy-t*aby
	return dx*dx + dy*dy
}
