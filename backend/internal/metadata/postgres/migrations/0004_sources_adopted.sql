-- An adopted source replaces the configuration-file entry of the same name,
-- so it can be edited in the UI. Without the flag the file always wins.
ALTER TABLE sources ADD COLUMN adopted boolean NOT NULL DEFAULT false;
