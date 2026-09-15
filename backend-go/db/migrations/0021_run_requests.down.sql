-- Dropping the reservation table only removes the idempotency guarantee for
-- FUTURE requests; every run/message written under a reservation stays.
DROP TABLE IF EXISTS run_requests;
