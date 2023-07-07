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
4. Run the app using main.go file.
5. Make requests!

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

---

## Google Drive

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

&nbsp;


---

## Google Photos

&nbsp;

`/photos-list-albums` (GET)

returns all the user's album names and their ID's on Google Photos.

&nbsp;

`/photos-list-photos-in-album/:ID` (GET)

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| name | Yes | album ID |

takes Google Photos album's ID and retreives data about photos in this album.

&nbsp;

`/storj-to-photos/:name` (GET)

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| name | Yes | photo name in Storj |

takes photo name as a parameter, downloads this file from Storj and uploads it to your Google Photos.

&nbsp;

`/photos-to-storj/:ID` (GET)

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| ID | Yes | photo ID in Google Photos |

takes photo ID as a parameter, downloads this file from Google Photos and uploads it to your Storj bucket.

&nbsp;

## Gmail

&nbsp;

`/gmail-list-threads` (GET)

returns list of user's threads.

&nbsp;

`/gmail-get-message/:ID` (GET)

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| ID | Yes | message (email) ID in Gmail |

takes message ID as a parameter and returns message.

&nbsp;

`/gmail-list-messages` (GET)

returns list of user's messages.

&nbsp;

`/gmail-message-to-storj/:ID` (GET)

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| ID | Yes | message (email) ID in Gmail |

takes message ID as a parameter and saves it to the backup database in Storj bucket.

&nbsp;

`/get-gmail-db-from-storj` (GET)

returns database file (gmails.db) with backuped data.

&nbsp;

## Google Cloud Storage

&nbsp;

`/storage-list-buckets/:projectName` (GET)

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| projectName | Yes | Google Cloud project name |

takes Google Cloud project name as a parameter, returns JSON responce with all the buckets in this project.

&nbsp;

`/storage-list-items/:bucketName` (GET)

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| bucketName | Yes | Google Cloud bucket name |

takes Google Cloud bucket name as a parameter, returns JSON responce with all the items in this bucket.

&nbsp;

`/storage-item-to-storj/:bucketName/:itemName` (GET)

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| bucketName | Yes | Google Cloud bucket name |
| itemName | Yes | Google Cloud item name |

takes bucket name and item name as a parameters, downloads the object from Google Cloud Storage and uploads it into Storj "google-cloud" bucket.

&nbsp;

`/storage-item-from-storj-to-google-cloud/:bucketName/:itemName` (GET)

| Parameter | Required |  Description |
| ----------- | ----------- |----------- |
| bucketName | Yes | Storj bucket name |
| itemName | Yes | Storj item name |

takes bucket name and item name as a parameters, downloads the object from Storj bucket and uploads it into Google Cloud Storage bucket.

&nbsp;