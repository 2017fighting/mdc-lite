package cli

import (
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/source"
)

func TestDescribeFieldRendersEveryKind(t *testing.T) {
	title, empty := "A Title", ""
	minutes, rating := 150, 8.5
	f := source.Fields{
		Title:        &title,
		Releasedate:  &empty, // a contribution whose value is empty still counts
		Runtime:      &minutes,
		Rating:       &rating,
		Tags:         []string{"tag1", "tag2"},
		Actors:       []meta.Actor{{Name: "葵つかさ", Gender: meta.Female}, {Name: "Actor B", Gender: meta.Male}},
		Fanart:       &source.ImageRef{Role: field.Fanart, URL: "https://site/cover.jpg"},
		Extrafanarts: []source.ImageRef{{URL: "https://site/1.jpg"}, {URL: "https://site/2.jpg"}},
	}
	cases := []struct {
		name        field.Name
		want        string
		contributed bool
	}{
		{field.Title, "A Title", true},
		{field.Releasedate, "", true},
		{field.Runtime, "150 分钟", true},
		{field.Rating, "8.50", true},
		{field.Tags, "tag1, tag2", true},
		{field.Actors, "葵つかさ (female), Actor B (male)", true},
		{field.Fanart, "https://site/cover.jpg", true},
		{field.Extrafanarts, "https://site/1.jpg, https://site/2.jpg", true},
		{field.Plot, "", false},
		{field.Poster, "", false},
	}
	for _, c := range cases {
		got, contributed := describeField(c.name, f)
		if contributed != c.contributed || got != c.want {
			t.Errorf("describeField(%s) = %q, %v; want %q, %v", c.name, got, contributed, c.want, c.contributed)
		}
	}
}

func TestDescribeFieldInventsNothing(t *testing.T) {
	var f source.Fields
	for _, name := range field.All() {
		if _, ok := describeField(name, f); ok {
			t.Errorf("an empty Fields contributed %q", name)
		}
	}
	if _, ok := describeField("nonsense", f); ok {
		t.Error("describeField invented a field")
	}
}

func TestDescribeFieldKeepsTheGenderOnEveryActor(t *testing.T) {
	f := source.Fields{Actors: []meta.Actor{{Name: "A", Gender: meta.Unknown}, {Name: "B", Gender: meta.Male}}}
	got, _ := describeField(field.Actors, f)
	if !strings.Contains(got, "A (unknown)") || !strings.Contains(got, "B (male)") {
		t.Errorf("actors = %q, want the gender on each name", got)
	}
}

func TestMarkImageDeliveryTurnsAnUndeliveredCoverIntoNoContribution(t *testing.T) {
	// An image field is judged by bytes in hand. The source offered an address and
	// the fetch failed, so the field row must not read as a contribution.
	fields := describeFields([]field.Name{field.Title, field.Fanart}, source.Fields{
		Fanart: &source.ImageRef{Role: field.Fanart, URL: "https://site/cover.jpg"},
	})
	images := []imageReport{{Role: string(field.Fanart), URL: "https://site/cover.jpg", Err: "403"}}
	markImageDelivery(fields, images)

	for _, f := range fields {
		if f.Name != field.Fanart {
			continue
		}
		if f.Contributed {
			t.Error("fanart reads as contributed although its bytes never arrived")
		}
		if !strings.Contains(f.Note, "bytes never arrived") {
			t.Errorf("fanart note = %q", f.Note)
		}
	}
}

func TestMarkImageDeliveryKeepsADeliveredCover(t *testing.T) {
	fields := describeFields([]field.Name{field.Fanart}, source.Fields{
		Fanart: &source.ImageRef{Role: field.Fanart, URL: "https://site/cover.jpg"},
	})
	images := []imageReport{{Role: string(field.Fanart), URL: "https://site/cover.jpg", Bytes: 10, Kind: "jpg"}}
	markImageDelivery(fields, images)
	for _, f := range fields {
		if f.Name == field.Fanart && (!f.Contributed || f.Note != "") {
			t.Errorf("fanart row = %+v, want it contributed with no note", f)
		}
	}
}
