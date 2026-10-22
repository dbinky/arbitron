package layout

import (
	"encoding/json"
	"math"
	"strings"
)

// groupBlock is a measured static stack followed by fixed live rows. Alternatives
// on a row share its maximum dimensions; concurrently visible rows are summed.
type groupBlock struct {
	texts   []GroupText
	overlay *Rect
	w, h    float64
}

func measureGroupBlocks(g Graph, m Measurements, c RendererContract) (map[string]groupBlock, error) {
	items := groupTextItems(g, c)
	byElement := map[string][]TextItem{}
	for _, item := range items {
		// Keys are canonical tuples; use the producer's element identities, never
		// delimiter splitting (element IDs can contain Unicode and JSON punctuation).
		var key []string
		if err := json.Unmarshal([]byte(item.Key), &key); err != nil {
			return nil, err
		}
		byElement[key[1]] = append(byElement[key[1]], item)
	}
	out := map[string]groupBlock{}
	for _, gid := range g.Groups.Order {
		scope := g.Groups.Groups[gid]
		ids := []string{GroupElementID(gid, "container", ""), GroupElementID(gid, "header", ""), GroupElementID(gid, "footer", "")}
		for _, task := range scope.Config.Tasks {
			role := "lane"
			if scope.Config.Form == "workflows" {
				role = "task"
			}
			ids = append(ids, GroupElementID(gid, role, task.Key))
		}
		for _, id := range ids {
			var b groupBlock
			rowSizes := map[string]labelSize{}
			var rowOrder []string
			for _, item := range byElement[id] {
				var key []string
				_ = json.Unmarshal([]byte(item.Key), &key)
				field := key[2]
				px := c.NodeTypeFontPx
				if item.Style == StyleNodeLabel {
					px = c.NodeLabelFontPx
				}
				if item.Style == StyleEdgeLabel {
					px = c.EdgeLabelFontPx
				}
				tm, err := metricForText(m, item.Key, item.Text, px, c.LineHeight, item.MaxWidth)
				if err != nil {
					return nil, err
				}
				w, h := ceilPx(tm.Width), ceilPx(tm.Height)
				if strings.HasPrefix(field, "overlay:") {
					parts := strings.Split(field, ":")
					row := parts[1]
					if row == "progress" || row == "badge" {
						row += ":" + parts[2]
					}
					// Redaction occupies the card's measured title, not a badge row.
					if row == "badge:hidden dependency" {
						continue
					}
					if _, ok := rowSizes[row]; !ok {
						rowOrder = append(rowOrder, row)
					}
					old := rowSizes[row]
					old.W = math.Max(old.W, w)
					old.H = math.Max(old.H, h)
					rowSizes[row] = old
				} else if field == "redactedTitle" {
					for i := range b.texts {
						if strings.HasSuffix(b.texts[i].Key, `,"title"]`) {
							b.texts[i].Box.W = math.Max(b.texts[i].Box.W, w)
							b.texts[i].Box.H = math.Max(b.texts[i].Box.H, h)
						}
					}
				} else {
					b.texts = append(b.texts, GroupText{Key: item.Key, Text: item.Text, Style: item.Style, Lines: tm.Lines, Box: Rect{W: w, H: h}})
				}
			}
			y := 0.0
			for i := range b.texts {
				t := &b.texts[i]
				t.Box.Y = y
				y += t.Box.H + c.NodeTextGapPx
				b.w = math.Max(b.w, t.Box.W)
			}
			if len(rowOrder) > 0 {
				r := Rect{Y: y}
				for _, row := range rowOrder {
					s := rowSizes[row]
					r.W = math.Max(r.W, s.W)
					r.H += s.H + c.NodeTextGapPx
				}
				r.H -= c.NodeTextGapPx
				b.overlay = &r
				b.w = math.Max(b.w, r.W)
				y += r.H + c.NodeTextGapPx
			}
			if y > 0 {
				y -= c.NodeTextGapPx
			}
			b.h = y
			out[id] = b
		}
	}
	return out, nil
}
