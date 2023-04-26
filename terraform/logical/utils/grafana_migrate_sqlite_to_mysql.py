import os
import sqlite3
import pymysql

# SQLite database file
sqlite_file = 'grafana.db'

# Check if SQLite file exists
if not os.path.isfile(sqlite_file):
    print("SQLite database file not found, skipping migration.")
    exit(0)

# MySQL connection details
mysql_host = os.environ['MYSQL_HOST']
mysql_user = os.environ['MYSQL_USER']
mysql_password = os.environ['MYSQL_PASSWORD']
mysql_db = os.environ['MYSQL_DB']

# Connect to SQLite and MySQL databases
sqlite_conn = sqlite3.connect(sqlite_file)
mysql_conn = pymysql.connect(host=mysql_host, user=mysql_user, password=mysql_password, database=mysql_db)

# Cursor for SQLite and MySQL
sqlite_cursor = sqlite_conn.cursor()
mysql_cursor = mysql_conn.cursor()

# Check if migration has already been performed
mysql_cursor.execute("CREATE TABLE IF NOT EXISTS migration_metadata (migration_status BOOLEAN);")
mysql_conn.commit()
mysql_cursor.execute("SELECT migration_status FROM migration_metadata;")
migration_status = mysql_cursor.fetchone()

if migration_status is None or not migration_status[0]:
    # Perform migration if not already done

    # Get a list of tables in the SQLite database
    sqlite_cursor.execute("SELECT name FROM sqlite_master WHERE type='table';")
    tables = sqlite_cursor.fetchall()

    # Iterate over each table
    for table in tables:
        table_name = table[0]

        # Skip migration of internal SQLite tables
        if table_name.startswith("sqlite_"):
            continue

        # Migrate table structure and data from SQLite to MySQL
        # ... (same code as in the previous migration script)

    # Update migration_metadata to indicate migration has been performed
    mysql_cursor.execute("INSERT INTO migration_metadata (migration_status) VALUES (1);")
    mysql_conn.commit()

    print("Migration completed successfully.")
else:
    print("Migration already performed, skipping.")

# Close SQLite and MySQL connections
sqlite_cursor.close()
sqlite_conn.close()
mysql_cursor.close()
mysql_conn.close()
