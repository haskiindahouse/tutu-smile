package tutu

import "strings"

// Interest tags narrow the destination pool: the traveler's pain is not «нет
// билетов», it is «сложно выбрать». Tags are curated and honest — a city is
// tagged only for what it is actually known for. Live prices do the rest.

// Interest is a machine tag; the frontend shows the human tile.
type Interest string

const (
	IntFishing Interest = "fishing" // рыбалка
	IntSwim    Interest = "swim"    // купаться
	IntFood    Interest = "food"    // покушать
	IntNature  Interest = "nature"  // природа
	IntMounts  Interest = "mountains"
	IntHistory Interest = "history"
	IntParty   Interest = "party"
	IntChill   Interest = "chill"  // спокойный отдых
	IntSpa     Interest = "spa"    // бани, санатории, термы
	IntInsta   Interest = "insta"  // инста-туры: фотогеничные локации
	IntExotic  Interest = "exotic" // экзотика: непохожее на дом
)

// Scope limits the pool geographically.
type Scope string

const (
	ScopeRF     Scope = "rf"
	ScopeAbroad Scope = "abroad"
	ScopeAny    Scope = "any"
)

type taggedCity struct {
	city   string
	abroad bool
	// visaFree: безвизовый въезд для граждан РФ (по состоянию таблицы;
	// лучший честный ориентир, не юридическая справка). Для городов РФ
	// поле не имеет смысла и не читается.
	visaFree bool
	tags     []Interest
}

// cityTags: the roulette's raw material. Kept small and defensible — every
// entry answers «почему сюда» одним словом.
var cityTags = []taggedCity{
	// --- Россия ---
	{"Сочи", false, false, []Interest{IntSwim, IntNature, IntMounts, IntParty, IntFood, IntSpa, IntInsta}},
	{"Калининград", false, false, []Interest{IntHistory, IntNature, IntFood, IntChill, IntInsta}},
	{"Казань", false, false, []Interest{IntFood, IntHistory, IntParty, IntSpa, IntInsta}},
	{"Санкт-Петербург", false, false, []Interest{IntHistory, IntFood, IntParty, IntInsta}},
	{"Петрозаводск", false, false, []Interest{IntNature, IntFishing, IntChill}},
	{"Астрахань", false, false, []Interest{IntFishing, IntFood}},
	{"Иркутск", false, false, []Interest{IntNature, IntFishing, IntChill, IntInsta, IntExotic}}, // Байкал
	{"Мурманск", false, false, []Interest{IntNature, IntFishing, IntExotic, IntInsta}},          // сияние, Териберка
	{"Пятигорск", false, false, []Interest{IntChill, IntNature, IntSpa, IntMounts}},
	{"Кисловодск", false, false, []Interest{IntChill, IntNature, IntSpa}},
	{"Нижний Новгород", false, false, []Interest{IntHistory, IntFood, IntInsta}},
	{"Ярославль", false, false, []Interest{IntHistory, IntChill}},
	{"Владимир", false, false, []Interest{IntHistory, IntChill}},
	{"Псков", false, false, []Interest{IntHistory, IntNature, IntChill}},
	{"Великий Новгород", false, false, []Interest{IntHistory, IntChill}},
	{"Волгоград", false, false, []Interest{IntHistory}},
	{"Краснодар", false, false, []Interest{IntFood, IntParty}},
	{"Самара", false, false, []Interest{IntSwim, IntParty, IntFood}},
	{"Екатеринбург", false, false, []Interest{IntParty, IntFood, IntHistory}},
	{"Уфа", false, false, []Interest{IntNature, IntFood, IntSpa}},
	{"Тюмень", false, false, []Interest{IntSpa, IntFood}},                            // горячие источники
	{"Барнаул", false, false, []Interest{IntNature, IntMounts, IntChill, IntExotic}}, // ворота Алтая
	{"Владивосток", false, false, []Interest{IntFood, IntNature, IntSwim, IntExotic, IntInsta}},
	{"Сортавала", false, false, []Interest{IntNature, IntFishing, IntChill, IntInsta}}, // Рускеала
	{"Дербент", false, false, []Interest{IntHistory, IntSwim, IntFood, IntMounts, IntExotic, IntInsta}},
	{"Махачкала", false, false, []Interest{IntMounts, IntFood, IntSwim, IntExotic}},
	{"Тверь", false, false, []Interest{IntFishing, IntNature, IntChill}},
	{"Рыбинск", false, false, []Interest{IntFishing, IntNature, IntChill}},
	{"Саратов", false, false, []Interest{IntFishing, IntSwim, IntFood}},
	{"Киров", false, false, []Interest{IntNature, IntFishing}},
	{"Кострома", false, false, []Interest{IntHistory, IntNature, IntChill}},
	{"Суздаль", false, false, []Interest{IntHistory, IntChill, IntFood, IntInsta}},
	{"Выборг", false, false, []Interest{IntHistory, IntNature, IntFood, IntInsta}},
	{"Вологда", false, false, []Interest{IntHistory, IntNature, IntFood}},
	{"Геленджик", false, false, []Interest{IntSwim, IntChill, IntNature}},
	{"Анапа", false, false, []Interest{IntSwim, IntChill}},
	{"Новороссийск", false, false, []Interest{IntSwim, IntHistory}},
	{"Чебоксары", false, false, []Interest{IntChill, IntFood, IntSwim}},
	{"Пермь", false, false, []Interest{IntNature, IntParty}},

	// --- Ближний безвиз ---
	{"Минск", true, true, []Interest{IntFood, IntHistory, IntChill}},
	{"Ереван", true, true, []Interest{IntFood, IntHistory, IntNature, IntMounts}},
	{"Тбилиси", true, true, []Interest{IntFood, IntParty, IntNature, IntSpa, IntInsta}},
	{"Батуми", true, true, []Interest{IntSwim, IntParty, IntFood, IntInsta}},
	{"Алматы", true, true, []Interest{IntMounts, IntNature, IntFood, IntInsta}},
	{"Астана", true, true, []Interest{IntFood, IntParty}},
	{"Баку", true, true, []Interest{IntHistory, IntFood, IntSwim, IntInsta}},
	{"Ташкент", true, true, []Interest{IntFood, IntHistory, IntExotic}},
	{"Самарканд", true, true, []Interest{IntHistory, IntExotic, IntInsta}},
	{"Бишкек", true, true, []Interest{IntMounts, IntNature, IntExotic}}, // Иссык-Куль рядом

	// --- Турция, Балканы ---
	{"Стамбул", true, true, []Interest{IntFood, IntHistory, IntParty, IntSwim, IntInsta}},
	{"Анталья", true, true, []Interest{IntSwim, IntChill, IntNature, IntInsta}},
	{"Белград", true, true, []Interest{IntFood, IntParty, IntHistory}},
	{"Тиват", true, true, []Interest{IntSwim, IntChill, IntNature, IntInsta}}, // Черногория

	// --- Азия ---
	{"Пекин", true, true, []Interest{IntHistory, IntFood, IntExotic, IntInsta}}, // безвиз РФ⇄КНР
	{"Шанхай", true, true, []Interest{IntFood, IntParty, IntExotic, IntInsta}},
	{"Бангкок", true, true, []Interest{IntFood, IntParty, IntExotic, IntInsta}},
	{"Пхукет", true, true, []Interest{IntSwim, IntChill, IntParty, IntExotic, IntInsta}},
	{"Нячанг", true, true, []Interest{IntSwim, IntFood, IntChill, IntExotic}},
	{"Дубай", true, true, []Interest{IntSwim, IntParty, IntInsta}},
	{"Мале", true, true, []Interest{IntSwim, IntChill, IntExotic, IntInsta}}, // Мальдивы

	// --- Африка ---
	{"Марракеш", true, true, []Interest{IntExotic, IntFood, IntHistory, IntInsta}},
	{"Шарм-эль-Шейх", true, false, []Interest{IntSwim, IntChill, IntExotic}}, // виза по прибытии
}

// InterestHuman maps tags to the tile labels the UI shows.
var InterestHuman = map[Interest]string{
	IntFishing: "рыбалка", IntSwim: "купаться", IntFood: "покушать",
	IntNature: "природа", IntMounts: "горы", IntHistory: "история",
	IntParty: "тусовки", IntChill: "отдых", IntSpa: "бани и спа",
	IntInsta: "инста-туры", IntExotic: "экзотика",
}

// CitiesByInterests returns cities matching AT LEAST ONE chosen interest
// within the scope, best matches (more overlapping tags) first. Empty
// interests = the whole scope pool. origin is excluded. visaFreeOnly keeps
// only cities RU citizens enter without a visa (домашние города проходят
// всегда — там визы нет по определению).
func CitiesByInterests(interests []Interest, scope Scope, origin string, visaFreeOnly bool) []string {
	want := map[Interest]bool{}
	for _, i := range interests {
		want[i] = true
	}
	type scored struct {
		city  string
		score int
	}
	var out []scored
	for _, tc := range cityTags {
		if scope == ScopeRF && tc.abroad {
			continue
		}
		if scope == ScopeAbroad && !tc.abroad {
			continue
		}
		if visaFreeOnly && tc.abroad && !tc.visaFree {
			continue
		}
		if strings.EqualFold(tc.city, strings.TrimSpace(origin)) {
			continue
		}
		score := 0
		for _, t := range tc.tags {
			if want[t] {
				score++
			}
		}
		if len(want) > 0 && score == 0 {
			continue
		}
		out = append(out, scored{tc.city, score})
	}
	// Stable order by score desc, then table order (already curated).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].score > out[j-1].score; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	cities := make([]string, len(out))
	for i, s := range out {
		cities[i] = s.city
	}
	return cities
}

// TagsOf returns the human tags of a city for the result card.
func TagsOf(city string) []string {
	for _, tc := range cityTags {
		if strings.EqualFold(tc.city, city) {
			out := make([]string, 0, len(tc.tags))
			for _, t := range tc.tags {
				out = append(out, InterestHuman[t])
			}
			return out
		}
	}
	return nil
}

// VisaInfoRU reports the visa situation for RU citizens: abroad=false means
// the question does not apply; visaFree is meaningful only when abroad.
func VisaInfoRU(city string) (abroad, visaFree bool) {
	for _, tc := range cityTags {
		if strings.EqualFold(tc.city, city) {
			return tc.abroad, tc.visaFree
		}
	}
	return false, false
}
