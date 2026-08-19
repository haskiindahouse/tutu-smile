package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// EventDraft is the structured expansion of a freeform event description —
// typed, dictated by voice, or pasted straight from a group chat. It mirrors
// the event constructor's fields so the frontend can prefill everything and
// the organizer only confirms.
type EventDraft struct {
	Name        string       `json:"name"`
	Destination string       `json:"destination"` // empty when the wish is a vibe
	Vibe        string       `json:"vibe"`        // freeform destination wish, if no concrete city
	Date        string       `json:"date"`        // YYYY-MM-DD
	Deadline    string       `json:"deadline"`    // HH:MM
	BufferHours float64      `json:"buffer_hours"`
	SpacingMin  int          `json:"spacing_min"`
	BudgetRub   int          `json:"budget_per_person"`
	Guests      []DraftGuest `json:"guests"`
	Missing     []string     `json:"missing"` // what the organizer still has to fill
	Note        string       `json:"note"`    // one line for the UI: what was understood
}

type DraftGuest struct {
	Name           string `json:"name"`
	City           string `json:"city"`
	Profile        string `json:"profile"` // cheaper | faster
	Adults         int    `json:"adults"`
	Children       int    `json:"children"`
	NeedsLodging   bool   `json:"needs_lodging"`
	FindCompanions bool   `json:"find_companions"`
}

// ParseEvent turns freeform text into an event draft. prior, when non-nil, is
// the draft the organizer is amending («добавь ещё Пашу из Перми») — the model
// merges instead of starting over. Requires the LLM; callers surface a clear
// "smart intake needs a key" message when disabled.
func (c *Client) ParseEvent(ctx context.Context, text string, prior *EventDraft, now time.Time) (*EventDraft, error) {
	if !c.enabled {
		return nil, fmt.Errorf("llm disabled: no OpenRouter key")
	}
	sys := "Ты — парсер сервиса «Улыбка», который собирает гостей события из разных городов к одному дедлайну. " +
		"Организатор описывает событие свободным текстом (напечатал, надиктовал голосом или вставил переписку из чата). " +
		"Верни СТРОГО JSON без пояснений по схеме: " +
		`{"name":"...","destination":"...","vibe":"...","date":"YYYY-MM-DD","deadline":"HH:MM","buffer_hours":N,"spacing_min":N,"budget_per_person":N,` +
		`"guests":[{"name":"...","city":"...","profile":"cheaper|faster","adults":N,"children":N,"needs_lodging":bool,"find_companions":bool}],` +
		`"missing":["..."],"note":"..."}` + "\n" +
		"Правила:\n" +
		"- destination — конкретный российский город события, если назван. Если города нет, а есть желание («к морю, недорого») — оставь destination пустым и положи желание в vibe.\n" +
		"- Сегодня " + now.Format("2006-01-02") + " (" + weekdayRu(now) + "). Относительные даты («в субботу», «через две недели») разворачивай в БУДУЩУЮ дату YYYY-MM-DD.\n" +
		"- deadline — час сбора HH:MM; если не назван, поставь 15:00 и добавь «час сбора» в missing.\n" +
		"- buffer_hours: 0 = не назван (сервис подставит дефолт). spacing_min: 0 = не назван.\n" +
		"- Гости: каждый человек/семья с городом выезда. «подешевле/бабушка/студент» → profile=cheaper, «побыстрее/занятой/летит» → faster; по умолчанию cheaper. " +
		"«с женой/вдвоём» → adults=2. Дети считаются в children. «с ночёвкой/нужна гостиница» → needs_lodging=true. «ищет попутчиков/пусть едут вместе» → find_companions=true.\n" +
		"- Из переписки чата вытаскивай гостей по репликам («я из Перми, прилечу») — имя бери из подписи или обращения.\n" +
		"- НИЧЕГО не выдумывай: чего нет в тексте — не заполняй, а перечисли по-русски в missing (например «город события», «дата»).\n" +
		"- note — одна фраза: что понял и что уточнить.\n" +
		"- Если передан ТЕКУЩИЙ ЧЕРНОВИК, это правка: верни объединённый черновик целиком (добавь/измени по тексту, остальное сохрани)."
	user := "Текст организатора: " + text
	if prior != nil {
		if b, err := json.Marshal(prior); err == nil {
			user += "\n\nТЕКУЩИЙ ЧЕРНОВИК (правь его, не начинай заново): " + string(b)
		}
	}
	raw, err := c.complete(ctx, sys, user, true)
	if err != nil {
		return nil, err
	}
	var draft EventDraft
	if err := json.Unmarshal([]byte(extractJSON(raw)), &draft); err != nil {
		return nil, fmt.Errorf("parse draft: %w", err)
	}
	normalizeDraft(&draft)
	return &draft, nil
}

func normalizeDraft(d *EventDraft) {
	d.Destination = strings.TrimSpace(d.Destination)
	d.Date = strings.TrimSpace(d.Date)
	d.Deadline = strings.TrimSpace(d.Deadline)
	if d.Deadline == "" {
		d.Deadline = "15:00"
	}
	for i := range d.Guests {
		g := &d.Guests[i]
		g.Name = strings.TrimSpace(g.Name)
		g.City = strings.TrimSpace(g.City)
		if g.Profile != "faster" {
			g.Profile = "cheaper"
		}
		if g.Adults < 1 {
			g.Adults = 1
		}
	}
	if d.Destination == "" && d.Vibe == "" {
		d.Missing = appendMissing(d.Missing, "город события или пожелание")
	}
	if d.Date == "" {
		d.Missing = appendMissing(d.Missing, "дата события")
	}
	if len(d.Guests) == 0 {
		d.Missing = appendMissing(d.Missing, "гости (город выезда)")
	}
}

func appendMissing(list []string, item string) []string {
	for _, m := range list {
		if strings.EqualFold(m, item) {
			return list
		}
	}
	return append(list, item)
}

func weekdayRu(t time.Time) string {
	switch t.Weekday() {
	case time.Monday:
		return "понедельник"
	case time.Tuesday:
		return "вторник"
	case time.Wednesday:
		return "среда"
	case time.Thursday:
		return "четверг"
	case time.Friday:
		return "пятница"
	case time.Saturday:
		return "суббота"
	default:
		return "воскресенье"
	}
}
