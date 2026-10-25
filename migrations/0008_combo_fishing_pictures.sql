-- +goose Up
-- The daily combo uses the app's fishing pictures (hat, fish, net, boots, boat, bucket, backpack, rod).
-- Ids 1-8 match the artwork bundled in the app; an admin can still rename them or point a picture at an image link.
UPDATE combo_images SET name_en='Hat',      name_ru='Шляпа',    icon='🎩' WHERE id=1;
UPDATE combo_images SET name_en='Fish',     name_ru='Рыба',     icon='🐟' WHERE id=2;
UPDATE combo_images SET name_en='Net',      name_ru='Сеть',     icon='🥅' WHERE id=3;
UPDATE combo_images SET name_en='Boots',    name_ru='Сапоги',   icon='🥾' WHERE id=4;
UPDATE combo_images SET name_en='Boat',     name_ru='Лодка',    icon='⛵' WHERE id=5;
UPDATE combo_images SET name_en='Bucket',   name_ru='Ведро',    icon='🪣' WHERE id=6;
UPDATE combo_images SET name_en='Backpack', name_ru='Рюкзак',   icon='🎒' WHERE id=7;
UPDATE combo_images SET name_en='Rod',      name_ru='Удочка',   icon='🎣' WHERE id=8;

-- +goose Down
UPDATE combo_images SET name_en='Apple',      name_ru='Яблоко',   icon='🍎' WHERE id=1;
UPDATE combo_images SET name_en='Banana',     name_ru='Банан',    icon='🍌' WHERE id=2;
UPDATE combo_images SET name_en='Pineapple',  name_ru='Ананас',   icon='🍍' WHERE id=3;
UPDATE combo_images SET name_en='Orange',     name_ru='Апельсин', icon='🍊' WHERE id=4;
UPDATE combo_images SET name_en='Grapes',     name_ru='Виноград', icon='🍇' WHERE id=5;
UPDATE combo_images SET name_en='Cherry',     name_ru='Вишня',    icon='🍒' WHERE id=6;
UPDATE combo_images SET name_en='Lemon',      name_ru='Лимон',    icon='🍋' WHERE id=7;
UPDATE combo_images SET name_en='Watermelon', name_ru='Арбуз',    icon='🍉' WHERE id=8;
