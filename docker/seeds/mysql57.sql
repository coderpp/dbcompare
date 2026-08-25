-- MySQL 5.7-only extras and intentional diffs vs 8.0.

SET NAMES utf8mb4;

-- Extra table only on 5.7 (appears as DELETE when syncing 8.0 -> 5.7 direction as source=8.0)
CREATE TABLE IF NOT EXISTS only_on_57 (
  id INT NOT NULL AUTO_INCREMENT,
  payload VARCHAR(64) NOT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- Partitioned InnoDB table (supported on both, used for partition awareness tests)
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
