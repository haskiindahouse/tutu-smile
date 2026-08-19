package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// CityPlan is a walking itinerary for a visited city, honest about its
// nature: these are LLM suggestions of well-known places (not live data), so
// the UI exports them to Google Maps where hours/routes are real.
type CityPlan struct {
	City string    `json:"city"`
	Days []PlanDay `json:"days"`
	Note string    `json:"note"`
}

type PlanDay struct {
	Title string     `json:"title"`
	Stops []PlanStop `json:"stops"`
}

type PlanStop struct {
	Name string `json:"name"` // резолвится Google Картами: «Название, Город»
	Why  string `json:"why"`  // одна строка: почему сюда
}

// CityPlan builds a per-day walking plan tuned to the traveler's interests.
// Falls back to a deterministic classic-route skeleton without the LLM.
func (c *Client) CityPlan(ctx context.Context, city string, interests []string, days int) (*CityPlan, error) {
	if days < 1 {
		days = 1
	}
	if !c.enabled {
		return fallbackPlan(city, days), nil
	}
	sys := "Ты — локальный гид сервиса «Улыбка». Составь пеший план по городу на " +
		fmt.Sprintf("%d", days) + " дн. Верни СТРОГО JSON: " +
		`{"city":"...","days":[{"title":"...","stops":[{"name":"...","why":"..."}]}],"note":"..."}` + "\n" +
		"Правила:\n" +
		"- 4-6 остановок в день, в порядке удобного пешего/короткого маршрута.\n" +
		"- name — РЕАЛЬНОЕ, широко известное место (достопримечательность, парк, рынок, набережная, известное заведение), " +
		"по которому Google Карты найдут точку по строке «название, город». Никаких выдуманных мест.\n" +
		"- why — одна короткая фраза.\n" +
		"- Интересы путешественника: " + strings.Join(interests, ", ") + " — подстрой остановки под них (рыбалка → водоёмы/снасти, покушать → известная местная еда).\n" +
		"- note — одна фраза-настроение плана. Часы работы не указывай."
	raw, err := c.complete(ctx, sys, "Город: "+city, true)
	if err != nil {
		return fallbackPlan(city, days), nil
	}
	var plan CityPlan
	if err := json.Unmarshal([]byte(extractJSON(raw)), &plan); err != nil || len(plan.Days) == 0 {
		return fallbackPlan(city, days), nil
	}
	if plan.City == "" {
		plan.City = city
	}
	if plan.Note != "" {
		plan.Note += " · план сгенерирован ИИ — точки открываются в Google Картах"
	} else {
		plan.Note = "план сгенерирован ИИ — точки открываются в Google Картах"
	}
	return &plan, nil
}

// Spot is one «впечатление» — a photogenic place/activity for the carousel.
// Wiki holds the photo, Google Maps holds the pin; we hold the vibe.
type Spot struct {
	Name  string `json:"name"` // known place, resolvable by Wikipedia & Maps
	Wiki  string `json:"wiki"` // Wikipedia article title for the photo lookup
	Emoji string `json:"emoji"`
	Why   string `json:"why"` // one hook line
}

// Spots returns the city's «лента впечатлений» tuned to interests.
func (c *Client) Spots(ctx context.Context, city string, interests []string) ([]Spot, error) {
	if !c.enabled {
		return nil, fmt.Errorf("llm disabled")
	}
	sys := "Ты собираешь ленту впечатлений города для тревел-сервиса. Верни СТРОГО JSON: " +
		`{"spots":[{"name":"...","wiki":"...","emoji":"…","why":"..."}]}` + "\n" +
		"Правила:\n" +
		"- 6-8 РЕАЛЬНЫХ фотогеничных мест/активностей города и окрестностей (до ~1.5 ч пути).\n" +
		"- name — как место называют люди. wiki — точное название статьи русской Википедии об этом месте " +
		"(если статьи скорее нет — статья о городе). emoji — один подходящий эмодзи. why — одна цепкая фраза без канцелярита.\n" +
		"- Интересы гостя: " + strings.Join(interests, ", ") + " — минимум половина мест под них.\n" +
		"- Никаких выдуманных мест."
	raw, err := c.complete(ctx, sys, "Город: "+city, true)
	if err != nil {
		return nil, err
	}
	var out struct {
		Spots []Spot `json:"spots"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil || len(out.Spots) == 0 {
		return nil, fmt.Errorf("spots parse failed")
	}
	return out.Spots, nil
}

func fallbackPlan(city string, days int) *CityPlan {
	day := PlanDay{
		Title: "Классический круг",
		Stops: []PlanStop{
			{Name: "Центральная площадь, " + city, Why: "старт — сердце города"},
			{Name: "Краеведческий музей, " + city, Why: "быстрое погружение в место"},
			{Name: "Центральный рынок, " + city, Why: "местная еда честнее ресторанов"},
			{Name: "Набережная, " + city, Why: "вечерняя прогулка"},
		},
	}
	p := &CityPlan{City: city, Note: "без ИИ — классический маршрут; точки открываются в Google Картах"}
	for i := 0; i < days; i++ {
		p.Days = append(p.Days, day)
	}
	return p
}
