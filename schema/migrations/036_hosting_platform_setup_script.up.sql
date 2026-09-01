-- A platform's setup instructions are two different things: prose a person
-- reads, and commands they paste into a terminal. Held in one column they were
-- necessarily one or the other, and the copy the user needed most -- the script
-- -- could not be offered as something to copy.
ALTER TABLE hosting_platforms ADD COLUMN setup_script TEXT NOT NULL DEFAULT '';
