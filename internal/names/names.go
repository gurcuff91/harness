// Package names generates the readable names Harness gives to things that
// need one: a colleague ("scorpion-hero") and a sub-agent. Kept apart so
// package agent (which launches sub-agents) and channels/colleague (which
// imports agent) can share the same lists without an import cycle.
package names

import randv2 "math/rand/v2"

// A name is a character and a trait from the Mortal Kombat universe, joined
// by a dash.
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

// Size is how many different names exist (characters × traits).
func Size() int { return len(characters) * len(traits) }

// Random draws a name ("scorpion-hero"). Seeded by the runtime per process, so
// two processes started together do not draw the same stream. A variable so a
// test can shrink the pool.
var Random = func() string {
	return characters[randv2.IntN(len(characters))] + "-" + traits[randv2.IntN(len(traits))]
}
