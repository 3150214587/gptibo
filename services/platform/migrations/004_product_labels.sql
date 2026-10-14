-- Adopt the platform's names without overwriting names already edited by an administrator.
UPDATE products SET name=CASE code
 WHEN 'gpt-plus' THEN 'Plus'
 WHEN 'gpt-5x' THEN '5x'
 WHEN 'gpt-10x' THEN '10x'
 WHEN 'gpt-25x' THEN '25x'
 END
WHERE (code,name) IN (('gpt-plus','ChatGPT Plus'),('gpt-5x','ChatGPT Pro 5×'),('gpt-10x','ChatGPT Pro 10×'),('gpt-25x','ChatGPT Pro 25×'));
