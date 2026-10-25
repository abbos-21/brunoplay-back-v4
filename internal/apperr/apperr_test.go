package apperr

import "testing"

func TestEveryCodeHasBothLanguages(t *testing.T) {
	for _, c := range Codes() {
		en, ru, ok := HasText(c)
		if !ok || en == "" || ru == "" {
			t.Errorf("code %s lacks a text (en=%q ru=%q)", c, en, ru)
		}
	}
}

func TestMessageParams(t *testing.T) {
	e := WdMin.With("min", 10000)
	if got := e.Message(EN); got != "Minimum withdrawal is 10000 coins" {
		t.Fatal(got)
	}
	if got := e.Message(RU); got != "Минимальный вывод — 10000 монет" {
		t.Fatal(got)
	}
}

func TestParseLang(t *testing.T) {
	for in, want := range map[string]Lang{"ru": RU, "RU": RU, "ru-RU,ru;q=0.9": RU, "en-US": EN, "": EN, "de": EN} {
		if got := ParseLang(in); got != want {
			t.Errorf("%q -> %s want %s", in, got, want)
		}
	}
}
