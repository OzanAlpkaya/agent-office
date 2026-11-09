package engine

import (
	"hash/fnv"
	"strconv"
)

// firstNames are the names agents get (ADR 0043): common first names from
// many languages and regions, about half usually given to women and half to
// men, short enough for an office nameplate, written in ASCII so they read
// the same in every font and terminal. The order is fixed: a name's position
// decides which agent ids land on it.
var firstNames = [...]string{
	// Europe
	"Emma", "Liam", "Olivia", "Noah", "Grace", "Jack", "Ruby", "Leo",
	"Sofia", "Mateo", "Lucia", "Diego", "Elena", "Pablo", "Chloe", "Hugo",
	"Camille", "Louis", "Lena", "Jonas", "Mila", "Finn", "Sanne", "Daan",
	"Astrid", "Erik", "Ingrid", "Lars", "Giulia", "Marco", "Chiara", "Luca",
	"Zofia", "Jakub", "Petra", "Milan", "Olga", "Ivan", "Eleni", "Nikos",
	"Aoife", "Cian", "Niamh", "Sean", "Reka", "Bence", "Ines", "Tiago",
	// Turkey, the Middle East and Central Asia
	"Elif", "Emre", "Zeynep", "Burak", "Selin", "Deniz", "Ayse", "Can",
	"Omar", "Layla", "Yusuf", "Amina", "Karim", "Nour", "Shirin", "Reza",
	"Noa", "Eitan", "Tamar", "Aigerim", "Timur", "Dilnoza",
	// South Asia
	"Priya", "Arjun", "Ananya", "Rohan", "Divya", "Vikram", "Meera", "Ravi",
	"Asha", "Kiran", "Farhan", "Nadia",
	// East and Southeast Asia
	"Wei", "Mei", "Jun", "Lin", "Hao", "Xin", "Yuki", "Haruto",
	"Aiko", "Kenji", "Riku", "Emi", "Jisoo", "Minjun", "Seoyeon", "Linh",
	"Minh", "Bao", "Putri", "Budi", "Dewi", "Malia",
	// Africa
	"Amara", "Kwame", "Zola", "Chidi", "Ngozi", "Kofi", "Thabo", "Zanele",
	"Abebe", "Wanjiru", "Femi", "Nia", "Tunde", "Imani", "Baraka", "Zuri",
	// The Americas and Oceania
	"Camila", "Thiago", "Valeria", "Rafael", "Beatriz", "Joaquin", "Aroha", "Tane",
	"Kai", "Maya",
}

// pickName returns a name for the agent with this id that taken does not
// contain. It starts at the position the id hashes to and takes the next free
// name in list order, so the choice is deterministic for a given id and set
// of taken names. When every name is taken, it numbers the hashed name
// ("Emma 2", "Emma 3", ...), skipping numbered names that are taken too.
func pickName(id string, taken func(string) bool) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	start := int(h.Sum32() % uint32(len(firstNames)))
	for i := range len(firstNames) {
		if n := firstNames[(start+i)%len(firstNames)]; !taken(n) {
			return n
		}
	}
	base := firstNames[start]
	for k := 2; ; k++ {
		if n := base + " " + strconv.Itoa(k); !taken(n) {
			return n
		}
	}
}
