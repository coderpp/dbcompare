-- Second schema on MySQL 8.0 for same-version sync integration tests.
CREATE DATABASE IF NOT EXISTS sync_target DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
USE sync_target;

-- Intentionally leaner than dst_db / common so compare finds adds.
CREATE TABLE IF NOT EXISTS users (
  id INT NOT NULL AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
