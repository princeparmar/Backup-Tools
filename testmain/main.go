package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/StorX2-0/Backup-Tools/apps/google"
	"github.com/StorX2-0/Backup-Tools/satellite"
	"google.golang.org/api/gmail/v1"
)

func main() {
	accessGrant := "1pXub2gPnUYRjr16uMzPPmcUjZrQwXFLAw8KP8k9MrHghwM5aNUYNHmNR713hyXoYyxLmnLkuAnVbpZKU1HJ8ZweBqfMA3Y9ALQVmXMjjA6v4eBHsvrNv32R84tTVLvmCaTQWywyX48LwAboEVKSg8M3b3SuGPRRaEJV7PuCYuidyx9KUat2bqucr3oHDZtomduGPvNdMXuSnpWQ34NWerzrg9nKBq9HQZSVkYVeBmNCz5xDrkzr7TdiDyqRheS2w"
	googleToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJnb29nbGVfdG9rZW4iOiJZYzRHaElaaFNsbk5RaXQ4UkM3SkV4aVd3dFM4N2UxM3JSRkRFT3pWNHdjcVV4U0RNMCIsInNlcnZpY2UyX3Rva2VuIjoiIiwiZXhwIjoxNzMxNTU3NTgzfQ.AUeZYEVMd59akgjIgAbPl659tU3iPv-yYR_jAlMERs0"
	key := "prince.soamedia@gmail.com/accounts@firefox.com - Account verified. Next, sync another device to finish setup - 1733cdecb6ddaf2d.gmail"

	gmailClient, err := google.NewGmailClientUsingToken(googleToken)
	if err != nil {
		panic(err)
	}

	// Download file from Satellite
	data, err := satellite.DownloadObject(context.Background(), accessGrant, satellite.ReserveBucket_Gmail, key)
	if err != nil {
		panic(err)
	}

	fmt.Println(string(data))

	// Parse the email data and insert into Gmail
	var gmailMsg gmail.Message
	if err := json.Unmarshal(data, &gmailMsg); err != nil {
		panic(err)
	}

	rawMessage := base64.RawURLEncoding.EncodeToString(data) // Use the original data for encoding
	gmailMsg.Raw = rawMessage                                // Set the raw message

	// Insert message into Gmail
	if err := gmailClient.InsertMessage(&gmailMsg); err != nil {
		panic(err)
	}
}
