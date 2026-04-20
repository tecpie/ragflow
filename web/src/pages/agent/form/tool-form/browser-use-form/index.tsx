import { FormContainer } from '@/components/form-container';
import {
  LargeModelFilterFormSchema,
  LargeModelFormField,
} from '@/components/large-model-form-field';
import {
  Form,
  FormControl,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { zodResolver } from '@hookform/resolvers/zod';
import { t } from 'i18next';
import { memo } from 'react';
import { useForm } from 'react-hook-form';
import { z } from 'zod';
import { FormWrapper } from '../../components/form-wrapper';
import { useValues } from '../use-values';
import { useWatchFormChange } from '../use-watch-change';

const FormSchema = z.object({
  llm_id: z.string().optional(),
  ...LargeModelFilterFormSchema,
  start_url: z.string().optional(),
  max_steps: z.coerce.number().int().min(1),
  timeout_sec: z.coerce.number().int().min(1),
  headless: z.boolean(),
});

function BrowserUseForm() {
  const values = useValues();

  const form = useForm<z.infer<typeof FormSchema>>({
    defaultValues: values,
    resolver: zodResolver(FormSchema),
  });

  useWatchFormChange(form);

  return (
    <Form {...form}>
      <FormWrapper>
        <FormContainer>
          <LargeModelFormField />
          <FormField
            control={form.control}
            name="start_url"
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('flow.startUrl')}</FormLabel>
                <FormControl>
                  <Input {...field} placeholder="https://example.com" />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="max_steps"
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('flow.maxSteps')}</FormLabel>
                <FormControl>
                  <Input type="number" {...field} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="timeout_sec"
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('flow.timeoutSec')}</FormLabel>
                <FormControl>
                  <Input type="number" {...field} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
          <FormField
            control={form.control}
            name="headless"
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('flow.headless')}</FormLabel>
                <FormControl>
                  <Switch
                    checked={field.value}
                    onCheckedChange={field.onChange}
                  />
                </FormControl>
                <FormMessage />
              </FormItem>
            )}
          />
        </FormContainer>
      </FormWrapper>
    </Form>
  );
}

export default memo(BrowserUseForm);
