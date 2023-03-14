# Storj Integrations Module

This is custom module for integration the most popular services with Storj decentralized cloud data storaging service. This service allowes to backup data to Storj and upload it back.

## Current Integrations list
- Google Drive


This service is currently under developing.

---

## Usage
1. Setup PostgreSQL database (no manual schema setup needed, app migrates all the tables and data automatically).
2. Create and write PostgreSQL connection data into `.env` file (see the `.env.example` file)
3. Create credentials for Application in Google auth service and put this file to general folder.
4. Make requests!

---

## Requests



`/storj-auth` (POST)

takes your authentication Storj key and returns it as a cookie for future requests.
| FormValue | Required |  Description |
| ----------- | ----------- |----------- |
| storj | Yes | Storj grant access token |

&nbsp;

`/google-auth` (GET)

redirects to google authentication module and in case of successful authentication saves data in database and returns cookie with authentication token.

&nbsp;

`/drive-get-file-names` (GET)

returns all the file names and their ID's on your Google Drive.

&nbsp;

`/drive-get-file/:ID` (GET)

takes file ID as a parameter, downloads this file and returs this file to you.

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| id | Yes | file ID, can be get using `/drive-get-file-names` request |

&nbsp;

`/drive-to-storj/:ID` (GET)

takes file ID as a parameter, downloads this file from Google Drive and uploads it to your Storj bucket.

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| id | Yes | file ID, can be get using `/drive-get-file-names` request |

&nbsp;

`/storj-to-drive/:name` (GET)

takes file name as a parameter, downloads this file from Storj and uploads it to your Google Drive.

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| name | Yes | file name |