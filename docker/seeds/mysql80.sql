-- MySQL 8.0-only features for capability / downgrade tests.

SET NAMES utf8mb4;

CREATE TABLE IF NOT EXISTS only_on_80 (
  id INT NOT NULL AUTO_INCREMENT,
  payload VARCHAR(64) NOT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- CHECK constraint (8.0.16+)
CREATE TABLE IF NOT EXISTS check_demo (
  id INT NOT NULL AUTO_INCREMENT,
  age INT NOT NULL,
  PRIMARY KEY (id),
  CONSTRAINT chk_age CHECK (age >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Invisible index + DESC index (8.0+)
CREATE TABLE IF NOT EXISTS index_demo_80 (
  id INT NOT NULL AUTO_INCREMENT,
  name VARCHAR(64) NOT NULL,
  score INT NOT NULL DEFAULT 0,
  PRIMARY KEY (id),
  KEY idx_score_desc (score DESC),
  KEY idx_name_invisible (name) INVISIBLE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Expression default (8.0.13+)
CREATE TABLE IF NOT EXISTS expr_default_demo (
  id INT NOT NULL AUTO_INCREMENT,
  meta JSON DEFAULT (JSON_OBJECT()),
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Functional / expression index (8.0.13+)
CREATE TABLE IF NOT EXISTS func_index_demo (
  id INT NOT NULL AUTO_INCREMENT,
  title VARCHAR(128) NOT NULL,
  PRIMARY KEY (id),
  KEY idx_title_upper ((UPPER(title)))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS part_sales (
  id INT NOT NULL,
  sale_date DATE NOT NULL,
  amount DECIMAL(10,2) NOT NULL DEFAULT 0,
  PRIMARY KEY (id, sale_date)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4
PARTITION BY RANGE (YEAR(sale_date)) (
  PARTITION p2020 VALUES LESS THAN (2021),
  PARTITION p2021 VALUES LESS THAN (2022),
  PARTITION pmax VALUES LESS THAN MAXVALUE
);
