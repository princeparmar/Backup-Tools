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
