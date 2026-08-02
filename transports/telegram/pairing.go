package telegram

import "fmt"

// Pair adds a chat id to the allowlist in ~/.harness/telegram.json. It's a pure
// config operation — no bot token or server needed. Idempotent.
func Pair(chatID int64) error {
	st, err := openStore("")
	if err != nil {
		return err
	}
	added, err := st.pair(chatID)
	if err != nil {
		return err
	}
	if added {
		fmt.Printf("Paired chat %d.\n", chatID)
	} else {
		fmt.Printf("Chat %d was already paired.\n", chatID)
	}
	return nil
}

// Unpair removes a chat id from the allowlist and drops its session mapping.
// Pure config operation.
func Unpair(chatID int64) error {
	st, err := openStore("")
	if err != nil {
		return err
	}
	removed, err := st.unpair(chatID)
	if err != nil {
		return err
	}
	if removed {
		fmt.Printf("Unpaired chat %d.\n", chatID)
	} else {
		fmt.Printf("Chat %d was not paired.\n", chatID)
	}
	return nil
}

// ListPaired prints the currently paired chat ids.
func ListPaired() error {
	st, err := openStore("")
	if err != nil {
		return err
	}
	ids := st.allowlist()
	if len(ids) == 0 {
		fmt.Println("No paired chats. Run 'harness telegram pair <chat_id>'.")
		return nil
	}
	fmt.Println("Paired chats:")
	for _, id := range ids {
		fmt.Printf("  %d\n", id)
	}
	return nil
}
