package colleague

import (
	"fmt"
	randv2 "math/rand/v2"
	"regexp"
)

// names: a character and a trait from the Mortal Kombat universe, the same
// pools the instance registry uses, joined by a dash ("scorpion-hero").
var characters = []string{
	"scorpion", "subzero", "raiden", "liukang", "kunglao", "kitana",
	"mileena", "jade", "sonya", "jax", "johnnycage", "kano", "baraka",
	"reptile", "smoke", "ermac", "noob", "cyrax", "sektor", "nightwolf",
	"jaxson", "kotal", "kung", "fujin", "shinnok", "quanchi", "sindel",
	"spawn", "robocop", "terminator", "joker", "rambo", "shangtsung",
	"shao", "goro", "motaro", "kintaro", "sheeva", "kabal", "frost",
	"cetrion", "kollector", "geras", "kronika", "noobsaibot",
}

var traits = []string{
	"warrior", "guardian", "protector", "spectre", "hellfire", "vengeance",
	"thunder", "god", "storm", "frost", "ice", "grandmaster", "monk",
	"dragon", "fire", "master", "hero", "temple", "star", "blade", "agent",
	"mercenary", "blackdragon", "tarkatan", "outworld", "edenton",
	"netherrealm", "chaos", "shadow", "revenant", "phantom", "ascended",
	"elder", "soul", "timekeeper",
}

// RandomID is a fresh colleague id ("scorpion-hero"). Seeded by the runtime
// per process, so two processes started together do not draw the same stream.
func RandomID() string { return randomID() }

// randomID draws an id; a variable so a test can shrink the pool.
var randomID = func() string {
	return characters[randv2.IntN(len(characters))] + "-" + traits[randv2.IntN(len(traits))]
}

// validID: lower-case letters, digits and dashes, 1–32 characters, starting
// and ending with a letter or a digit. It becomes part of a file name.
var validID = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

// checkID reports whether id can name a colleague.
func checkID(id string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid colleague id %q: use 1-32 of a-z, 0-9 and -, starting and ending with a letter or digit", id)
	}
	return nil
}
